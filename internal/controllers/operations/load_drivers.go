package operations

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
)

const driversLoadedAnnotation = "weka.io/drivers-loaded"

func getExpectedDriversVersion(image, bootId string) string {
	return fmt.Sprintf("%s:%s", image, bootId)
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

func DriversLoaded(node *v1.Node, image string, isFrontend bool) bool {
	annotations := node.Annotations
	current, ok := annotations[driversLoadedAnnotation]
	if ok && !isFrontend {
		return true
	}
	if current == getExpectedDriversVersion(image, node.Status.NodeInfo.BootID) {
		return true
	}
	return false
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
	isFrontend          bool // defines whether we should enforce latest version, or suffice with any version
	force               bool // ignores existing node annotation
}

func NewLoadDrivers(mgr ctrl.Manager, node *v1.Node, ownerDetails weka.WekaOwnerDetails,
	DriversLoaderImage string, DriversBuildId *string,
	distServiceEndpoint string, isFrontend, force bool) *LoadDrivers {
	kclient := mgr.GetClient()
	ns, _ := util.GetPodNamespace()
	return &LoadDrivers{
		mgr:                 mgr,
		client:              kclient,
		kubeService:         kubernetes.NewKubeService(kclient),
		scheme:              mgr.GetScheme(),
		containerDetails:    ownerDetails,
		driversLoaderImage:  DriversLoaderImage,
		driversBuildId:      DriversBuildId,
		node:                node,
		distServiceEndpoint: distServiceEndpoint,
		namespace:           ns,
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
		&lifecycle.SimpleStep{Name: "UpdateContainerImage", Run: o.UpdateContainerImage, Predicates: lifecycle.Predicates{o.imageHasChanged}},
		&lifecycle.SimpleStep{Name: "HandleNodeReboot", Run: o.HandleNodeReboot, Predicates: lifecycle.Predicates{o.NodeRebooted}},
		&lifecycle.SimpleStep{Name: "CleanupIfLoaded", Run: o.DeleteContainers, Predicates: lifecycle.Predicates{o.IsLoaded}, FinishOnSuccess: true},
		//TODO: We might be deleting container created by client here, IsLoaded would be true on mismatch. Just timing wise, this is unlikely to happen, as backends supposed to be upgraded
		&lifecycle.SimpleStep{Name: "CreateContainer", Run: o.CreateContainer, Predicates: lifecycle.Predicates{o.HasNotContainer}},
		&lifecycle.SimpleStep{Name: "PollResults", Run: o.PollResults},
		&lifecycle.SimpleStep{Name: "ProcessResult", Run: o.ProcessResult},
		&lifecycle.SimpleStep{Name: "DeleteContainers", Run: o.DeleteContainers},
	}
}

func (o *LoadDrivers) GetJsonResult() string {
	panic("not implemented due to no interfaced use")
}

func (o *LoadDrivers) HandleNodeReboot(ctx context.Context) error {
	annotations := o.node.Annotations
	if annotations == nil {
		return nil
	}
	if _, ok := annotations[driversLoadedAnnotation]; ok {
		delete(annotations, driversLoadedAnnotation)
		o.node.Annotations = annotations
		err := o.client.Update(ctx, o.node)
		if err != nil {
			err = errors.Wrap(err, "failed to update node annotations")
			return lifecycle.NewWaitError(err)
		}
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

func (o *LoadDrivers) IsLoaded() bool {
	if o.force {
		return false
	}
	return DriversLoaded(o.node, o.containerDetails.Image, o.isFrontend)
}

func (o *LoadDrivers) GetExpectedDriversVersion() string {
	return getExpectedDriversVersion(o.containerDetails.Image, o.node.Status.NodeInfo.BootID)
}

func (o *LoadDrivers) ExitIfLoaded(ctx context.Context) error {
	return nil
}

func (o *LoadDrivers) imageHasChanged() bool {
	if o.container == nil {
		return false
	}
	return o.container.Spec.Image != o.containerDetails.Image
}

func (o *LoadDrivers) UpdateContainerImage(ctx context.Context) error {
	o.container.Spec.Image = o.containerDetails.Image
	o.container.Spec.ImagePullSecret = o.containerDetails.ImagePullSecret
	err := o.client.Update(ctx, o.container)
	if err != nil {
		return err
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

func (o *LoadDrivers) HasNotContainer() bool {
	return o.container == nil
}

func (o *LoadDrivers) CreateContainer(ctx context.Context) error {
	serviceAccountName := config.Config.MaintenanceSaName
	name := o.getContainerName()

	labels := map[string]string{
		"weka.io/mode": weka.WekaContainerModeDriversLoader, // need to make this somehow more generic and not per place
	}
	labels = util.MergeMaps(o.containerDetails.Labels, labels)

	containerImage := o.containerDetails.Image
	var instructions *weka.Instructions
	if o.driversLoaderImage != "" {
		containerImage = o.driversLoaderImage
		instructions = &weka.Instructions{
			Type:    weka.InstructionCopyWekaFilesToContainer,
			Payload: o.containerDetails.Image,
		}
	}

	loaderContainer := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: o.namespace,
			Labels:    labels,
		},
		Spec: weka.WekaContainerSpec{
			Image:               containerImage,
			Instructions:        instructions,
			Mode:                weka.WekaContainerModeDriversLoader,
			ImagePullSecret:     o.containerDetails.ImagePullSecret,
			Hugepages:           0,
			NodeAffinity:        weka.NodeName(o.node.Name),
			DriversDistService:  o.distServiceEndpoint,
			DriversLoaderImage:  o.driversLoaderImage,
			DriversBuildId:      o.driversBuildId,
			TracesConfiguration: weka.GetDefaultTracesConfiguration(),
			Tolerations:         o.containerDetails.Tolerations,
			ServiceAccountName:  serviceAccountName,
		},
	}

	err := o.client.Create(ctx, loaderContainer)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			return lifecycle.NewWaitError(fmt.Errorf("container already exists"))
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
		_ = o.DeleteContainers(ctx)
		return NewDriversNotLoadedError(ret)
	}

	if !loadResults.Loaded {
		_ = o.DeleteContainers(ctx)
		return NewDriversNotLoadedError(nil)
	}

	annotations := o.node.Annotations
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[driversLoadedAnnotation] = o.GetExpectedDriversVersion()
	o.node.Annotations = annotations
	err = o.client.Update(ctx, o.node)
	if err != nil {
		return lifecycle.NewWaitError(err)
	}

	return nil
}

func (o *LoadDrivers) DeleteContainers(ctx context.Context) error {
	if o.container != nil {
		err := o.client.Delete(ctx, o.container)
		if err != nil {
			return err
		}
	}
	return nil
}
