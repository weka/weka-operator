package operations

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/drivers"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
)

const driversLoadedAnnotation = "weka.io/drivers-loaded"

// driverPriorityLabel records the priority rank of the container a loader is
// loading for, so a concurrent reconcile can order itself against an in-flight
// loader (see compareDriverOrder) without re-deriving the rank.
const driverPriorityLabel = "weka.io/driver-priority"

// driverBootIDLabel records the node boot id a loader was created for. After a
// reboot the node's kernel modules are gone and a leftover loader carries a
// pre-reboot ExecutionResult (a persisted CR status field); the stamp lets a
// reconcile tell such a stale loader from one freshly created for the current
// boot, so the stale one is deleted rather than re-recorded as loaded.
const driverBootIDLabel = "weka.io/driver-boot-id"

// loadedDrivers is the value stored in the drivers-loaded node annotation. A node
// has exactly one loaded driver version per boot; the record tracks which image
// was loaded and the priority of the container that dictated it, so a higher-order
// caller (strict frontend, or a newer version) can decide whether to preempt.
type loadedDrivers struct {
	BootID   string `json:"boot_id"`
	Image    string `json:"image"`
	Priority int    `json:"priority"`
}

// parseLoadedDrivers reads the drivers-loaded annotation. It understands both the
// current JSON format and the legacy "image:bootId" single-value format (so
// drivers loaded before this operator version are not re-loaded needlessly); a
// legacy record carries no priority (→ 0).
func parseLoadedDrivers(node *v1.Node) *loadedDrivers {
	raw, ok := node.Annotations[driversLoadedAnnotation]
	if !ok || raw == "" {
		return nil
	}
	var ld loadedDrivers
	if err := json.Unmarshal([]byte(raw), &ld); err == nil && ld.BootID != "" {
		return &ld
	}
	// legacy "image:bootId" format; image may itself contain ':' (tag), so split
	// on the last ':'
	idx := strings.LastIndex(raw, ":")
	if idx <= 0 {
		return nil
	}
	return &loadedDrivers{BootID: raw[idx+1:], Image: raw[:idx]}
}

// compareDriverOrder imposes the total order used to select the node's single
// loaded driver: priority first, then weka version. Returns >0 if (prioA,imgA)
// outranks (prioB,imgB), <0 if outranked, 0 if equal (including unparsable /
// equal versions at the same priority).
func compareDriverOrder(prioA int, imgA string, prioB int, imgB string) int {
	if prioA != prioB {
		if prioA > prioB {
			return 1
		}
		return -1
	}
	return utils.CompareVersions(utils.GetSoftwareVersion(imgA), utils.GetSoftwareVersion(imgB))
}

// DriverDecision is the outcome of evaluating a container against the node's
// currently-loaded driver record.
type DriverDecision int

const (
	DriverLoad      DriverDecision = iota // nothing valid loaded, or we preempt → load our version
	DriverSatisfied                       // our exact image is already loaded
	DriverDefer                           // lenient: some valid version is loaded, tolerate it
	DriverConflict                        // strict: a higher-order version is loaded, we cannot get ours
)

// EvaluateDrivers decides what a container should do given the node's current
// loaded-driver record. isFrontend marks a strict caller (needs its exact driver
// version); non-frontends are lenient (any loaded version suffices). The returned
// string is the currently-loaded image ("" when nothing valid is loaded).
func EvaluateDrivers(node *v1.Node, image string, priority int, isFrontend bool) (decision DriverDecision, img string) {
	ld := parseLoadedDrivers(node)
	if ld == nil || ld.BootID != node.Status.NodeInfo.BootID || ld.Image == "" {
		return DriverLoad, ""
	}
	if ld.Image == image {
		return DriverSatisfied, ld.Image
	}
	if !isFrontend {
		// lenient: backends/ssdproxy run fine on any loaded driver version
		return DriverDefer, ld.Image
	}
	order := compareDriverOrder(priority, image, ld.Priority, ld.Image)
	if order > 0 {
		// strict: we outrank what's loaded, so preempt and load our exact version
		return DriverLoad, ld.Image
	}
	if order == 0 {
		// strict, but the loaded driver has the same priority and weka version —
		// only the image string differs (e.g. a different registry/mirror, a
		// tag→digest swap, or a rebuilt base image). The loaded drivers are
		// compatible, so tolerate them rather than deadlock on a reload we cannot win.
		return DriverDefer, ld.Image
	}
	// strict: a higher-order version is loaded and it isn't ours — genuine conflict
	return DriverConflict, ld.Image
}

// loaderPriorityFromLabels reads the priority rank stamped on a loader container.
// Loaders created by this operator always carry it; anything else (legacy) → 0.
func loaderPriorityFromLabels(c *weka.WekaContainer) int {
	if c.Labels == nil {
		return 0
	}
	if v, ok := c.Labels[driverPriorityLabel]; ok {
		if p, err := strconv.Atoi(v); err == nil {
			return p
		}
	}
	return 0
}

// loaderBootIDFromLabels reads the boot id a loader container was created for.
// Loaders created by this operator stamp it (see driverBootIDLabel); anything
// without it (legacy) → "", which never equals a live boot id and so is always
// treated as stale.
func loaderBootIDFromLabels(c *weka.WekaContainer) string {
	if c.Labels == nil {
		return ""
	}
	return c.Labels[driverBootIDLabel]
}

type DriversNotLoadedError struct {
	Err error
}

func NewDriversNotLoadedError(err error) *DriversNotLoadedError {
	return &DriversNotLoadedError{Err: err}
}

func (e *DriversNotLoadedError) Error() string {
	return fmt.Sprintf("DriversNotLoadedError: %v", e.Err)
}

type LoadDrivers struct {
	mgr                 ctrl.Manager
	client              client.Client
	kubeService         kubernetes.KubeService
	scheme              *runtime.Scheme
	containerDetails    weka.WekaOwnerDetails
	driversLoaderImage  string
	driversBuildId      *string
	node                *v1.Node
	distServiceEndpoint string
	container           *weka.WekaContainer
	namespace           string
	priority            int  // rank in the (priority, version) total order used to pick the node driver
	isFrontend          bool // strict caller: requires its exact version rather than tolerating any
	force               bool // ignores existing node annotation
}

func NewLoadDrivers(mgr ctrl.Manager, node *v1.Node, ownerDetails weka.WekaOwnerDetails, //nolint:gocritic // intentional code pattern, linter suggestion does not apply here
	driversLoaderImage string, driversBuildId *string,
	distServiceEndpoint string, priority int, isFrontend, force bool) *LoadDrivers {
	kclient := mgr.GetClient()
	ns, _ := util.GetPodNamespace() //nolint:errcheck // namespace used for object metadata only; failure falls back to empty string
	return &LoadDrivers{
		mgr:                 mgr,
		client:              kclient,
		kubeService:         kubernetes.NewKubeService(kclient),
		scheme:              mgr.GetScheme(),
		containerDetails:    ownerDetails,
		driversLoaderImage:  driversLoaderImage,
		driversBuildId:      driversBuildId,
		node:                node,
		distServiceEndpoint: distServiceEndpoint,
		namespace:           ns,
		priority:            priority,
		isFrontend:          isFrontend,
		force:               force,
	}
}

func (o *LoadDrivers) AsStep() lifecycle.Step {
	return &lifecycle.SimpleStep{
		Name: "LoadDrivers",
		Run:  AsRunFunc(o),
	}
}

func (o *LoadDrivers) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SimpleStep{Name: "GetCurrentContainer", Run: o.GetCurrentContainer},
		&lifecycle.SimpleStep{Name: "HandleNodeReboot", Run: o.HandleNodeReboot, Predicates: lifecycle.Predicates{o.NodeRebooted}},
		&lifecycle.SimpleStep{Name: "CleanupIfLoaded", Run: o.CleanupIfLoaded, Predicates: lifecycle.Predicates{o.IsLoaded}, FinishOnSuccess: true},
		&lifecycle.SimpleStep{Name: "HandleExistingLoader", Run: o.HandleExistingLoader, Predicates: lifecycle.Predicates{o.HasContainer}},
		&lifecycle.SimpleStep{Name: "CreateContainer", Run: o.CreateContainer, Predicates: lifecycle.Predicates{o.HasNotContainer}},
		&lifecycle.SimpleStep{Name: "PollResults", Run: o.PollResults},
		&lifecycle.SimpleStep{Name: "ProcessResult", Run: o.ProcessResult},
		&lifecycle.SimpleStep{Name: "DeleteContainers", Run: o.DeleteContainers},
	}
}

func (o *LoadDrivers) GetJsonResult() string {
	panic("not implemented due to no interfaced use")
}

// HandleNodeReboot clears the drivers-loaded record left by the previous boot so
// drivers are re-loaded for the current one. A stale loader left over from a
// previous boot is deleted in HandleExistingLoader instead — that runs whenever a
// loader exists rather than only behind the NodeRebooted() predicate, so the stale
// check holds even when discovery info is missing (see HandleExistingLoader).
func (o *LoadDrivers) HandleNodeReboot(ctx context.Context) error {
	annotations := o.node.Annotations
	if annotations == nil {
		return nil
	}
	if _, ok := annotations[driversLoadedAnnotation]; !ok {
		return nil
	}
	delete(annotations, driversLoadedAnnotation)
	o.node.Annotations = annotations
	if err := o.client.Update(ctx, o.node); err != nil {
		return lifecycle.NewWaitError(errors.Wrap(err, "failed to update node annotations"))
	}
	return nil
}

func (o *LoadDrivers) NodeRebooted() bool {
	annotations := o.node.Annotations
	// compare boot id of the node with the boot id in annotation:
	// weka.io/discovery.json: '{"boot_id":"589e6771-6d16-47d3-be1c-d879812bb09f","schema":2,"num_cpus":11, ...}'
	discoveryRes, ok := annotations[discovery.DiscoveryAnnotation]
	if !ok {
		return false
	}
	discoveryNodeInfo := &discovery.DiscoveryNodeInfo{}
	err := json.Unmarshal([]byte(discoveryRes), discoveryNodeInfo)
	if err != nil {
		// if we cannot unmarshal the discovery json, assume the node just booted
		return true
	}
	return discoveryNodeInfo.BootID != o.node.Status.NodeInfo.BootID
}

// IsLoaded reports whether our exact image's drivers are already recorded as
// loaded for the current boot. force ignores the annotation (always reload).
func (o *LoadDrivers) IsLoaded() bool {
	if o.force {
		return false
	}
	ld := parseLoadedDrivers(o.node)
	return ld != nil && ld.BootID == o.node.Status.NodeInfo.BootID && ld.Image == o.containerDetails.Image
}

// CleanupIfLoaded removes a leftover loader once our drivers are loaded. It runs
// only when IsLoaded() holds (our exact version is recorded), so deleting any
// loader still present is safe — there is no not-yet-recorded load to interrupt.
func (o *LoadDrivers) CleanupIfLoaded(ctx context.Context) error {
	return o.DeleteContainers(ctx)
}

// HandleExistingLoader resolves the race when a loader already exists on the node.
// It orders the in-flight loader against our own by (priority, then version):
//   - stale boot id    → delete it and create ours (o.container = nil).
//   - same image       → keep it; we poll it to completion.
//   - we outrank it     → preempt: delete it and create ours (o.container = nil).
//   - otherwise        → defer to the >=-order load in flight (requeue).
func (o *LoadDrivers) HandleExistingLoader(ctx context.Context) error {
	// A loader whose boot-id stamp does not match the node's current boot id is
	// stale: the reboot unloaded its drivers and its persisted ExecutionResult
	// predates the reboot, so reusing it (on the image match below) would let
	// PollResults/ProcessResult re-record drivers as loaded without an actual
	// reload. This runs whenever a loader exists — unlike reboot detection, which
	// is gated on the discovery annotation and is missed when it is absent — so it
	// is the single place that guarantees a stale loader is never polled. A legacy
	// loader (no stamp → "") never matches a live boot id and is likewise stale.
	if loaderBootIDFromLabels(o.container) != o.node.Status.NodeInfo.BootID {
		if err := o.DeleteContainers(ctx); err != nil {
			return err
		}
		o.container = nil
		return nil
	}
	loaderImage := o.container.Spec.Image
	if loaderImage == o.containerDetails.Image {
		return nil
	}
	loaderPriority := loaderPriorityFromLabels(o.container)
	if compareDriverOrder(o.priority, o.containerDetails.Image, loaderPriority, loaderImage) > 0 {
		if err := o.DeleteContainers(ctx); err != nil {
			return err
		}
		o.container = nil
		return nil
	}
	return lifecycle.NewWaitErrorWithDuration(fmt.Errorf(
		"drivers loader for image %s (priority %d) is in flight, deferring load of %s (priority %d)",
		loaderImage, loaderPriority, o.containerDetails.Image, o.priority), time.Second*10)
}

// recordDriversLoaded sets the node's single loaded-drivers record for this boot.
func (o *LoadDrivers) recordDriversLoaded(ctx context.Context, image string, priority int) error {
	ld := &loadedDrivers{
		BootID:   o.node.Status.NodeInfo.BootID,
		Image:    image,
		Priority: priority,
	}
	raw, err := json.Marshal(ld)
	if err != nil {
		return err
	}
	if o.node.Annotations == nil {
		o.node.Annotations = make(map[string]string)
	}
	o.node.Annotations[driversLoadedAnnotation] = string(raw)
	if err := o.client.Update(ctx, o.node); err != nil {
		return fmt.Errorf("updating %s annotation: %w", driversLoadedAnnotation, err)
	}
	return nil
}

func (o *LoadDrivers) getContainerName() string {
	return fmt.Sprintf("weka-drivers-loader-%s", o.node.UID)
}

func (o *LoadDrivers) GetCurrentContainer(ctx context.Context) error {
	name := o.getContainerName()
	ref := weka.ObjectReference{
		Name:      name,
		Namespace: o.namespace,
	}

	existing, err := discovery.GetContainerByName(ctx, o.client, ref)
	if err != nil && apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("no weka container with name %s was found", name)
	}
	o.container = existing
	return nil
}

func (o *LoadDrivers) HasContainer() bool {
	return o.container != nil
}

func (o *LoadDrivers) HasNotContainer() bool {
	return o.container == nil
}

func (o *LoadDrivers) CreateContainer(ctx context.Context) error {
	serviceAccountName := config.Config.MaintenanceSaName
	name := o.getContainerName()
	loaderImage := drivers.GetLoaderImageForNode(ctx, o.node, o.containerDetails.Image)

	labels := map[string]string{
		"weka.io/mode":      weka.WekaContainerModeDriversLoader, // need to make this somehow more generic and not per place
		driverPriorityLabel: strconv.Itoa(o.priority),
		driverBootIDLabel:   o.node.Status.NodeInfo.BootID, // boot this loader is loading for; a reboot makes it stale
	}
	labels = util.MergeMaps(o.containerDetails.Labels, labels)

	// When loader image differs from cluster image, we need to copy weka files
	// from the cluster image via an init container
	var instructions *weka.Instructions
	if loaderImage != o.containerDetails.Image {
		payloadBytes, _ := json.Marshal(map[string]string{ //nolint:errcheck // marshal of string map; error not possible
			"targetImage": o.containerDetails.Image,
			"cliImage":    loaderImage,
		})
		instructions = &weka.Instructions{
			Type:    weka.InstructionCopyWekaFilesToDriverLoader,
			Payload: string(payloadBytes),
		}
	}

	loaderContainer := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: o.namespace,
			Labels:    labels,
		},
		Spec: weka.WekaContainerSpec{
			Image:               o.containerDetails.Image, // Always the cluster image
			Mode:                weka.WekaContainerModeDriversLoader,
			ImagePullSecret:     o.containerDetails.ImagePullSecret,
			Hugepages:           0,
			NodeAffinity:        weka.NodeName(o.node.Name),
			DriversDistService:  o.distServiceEndpoint,
			DriversLoaderImage:  loaderImage, // The actual image to use for the pod
			DriversBuildId:      o.driversBuildId,
			TracesConfiguration: weka.GetDefaultTracesConfiguration(),
			Tolerations:         o.containerDetails.Tolerations,
			ServiceAccountName:  serviceAccountName,
			Instructions:        instructions,
		},
	}

	err := o.client.Create(ctx, loaderContainer)
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			// another WekaContainer created the loader first; requeue so the next
			// pass fetches and drives the existing loader
			return lifecycle.NewWaitError(fmt.Errorf("drivers loader %s already exists", name))
		}
		return err
	}
	o.container = loaderContainer
	return nil
}

func (o *LoadDrivers) PollResults(ctx context.Context) error {
	if o.container.Status.ExecutionResult == nil {
		return lifecycle.NewWaitErrorWithDuration(fmt.Errorf("container execution result is not ready"), time.Second*10)
	}
	return nil
}

type DriveLoadResults struct {
	Err    string `json:"err"`
	Loaded bool   `json:"drivers_loaded"`
}

func (o *LoadDrivers) ProcessResult(ctx context.Context) error {
	loadResults := &DriveLoadResults{}
	err := json.Unmarshal([]byte(*o.container.Status.ExecutionResult), loadResults)
	if err != nil {
		return errors.Wrap(err, "Failed to unmarshal results")
	}

	if loadResults.Err != "" {
		ret := fmt.Errorf("%s, re-create container", loadResults.Err)
		_ = o.DeleteContainers(ctx) //nolint:errcheck // best-effort cleanup; returning primary error
		return NewDriversNotLoadedError(ret)
	}

	if !loadResults.Loaded {
		_ = o.DeleteContainers(ctx) //nolint:errcheck // best-effort cleanup; returning primary error
		return NewDriversNotLoadedError(errors.New("drivers loader reported drivers not loaded, re-create container"))
	}

	// Record the image this loader actually loaded (its own spec image) together
	// with the priority stamped on the loader — that is the rank that dictated the
	// node's driver version, and later callers order preemption against it.
	loadedImage := o.container.Spec.Image
	loadedPriority := loaderPriorityFromLabels(o.container)
	if err = o.recordDriversLoaded(ctx, loadedImage, loadedPriority); err != nil {
		return lifecycle.NewWaitError(err)
	}

	return nil
}

func (o *LoadDrivers) DeleteContainers(ctx context.Context) error {
	if o.container != nil {
		err := o.client.Delete(ctx, o.container)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}
