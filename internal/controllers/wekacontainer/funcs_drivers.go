// This file contains functions related to drivers loading and building during WekaContainer reconciliation
package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util/podexec"
)

func (r *containerReconcilerLoop) EnsureDrivers(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	if r.node == nil {
		return errors.New("node not found")
	}

	details := r.container.ToOwnerDetails()
	if r.container.Spec.DriversLoaderImage == "" && r.IsNotAlignedImage() {
		// do not create pod with spec image if we know in advance that we cannot upgrade
		canUpgrade, err := r.upgradeConditionsPass(ctx)
		if err != nil || !canUpgrade {
			logger.Debug("Cannot upgrade to new image, using last applied", "image", details.Image, "error", err)
			details.Image = r.container.Status.LastAppliedImage
		}
	}

	if r.pod != nil {
		wekaPodContainer, err := resources.GetWekaPodContainer(r.pod)
		if err != nil {
			return err
		}

		details.Image = wekaPodContainer.Image
	}

	priority := driverPriority(r.container)
	isFrontend := r.container.HasFrontend()

	decision, loadedImage := operations.EvaluateDrivers(r.node, details.Image, priority, isFrontend)
	var blocker string
	// Non-nil only once we have actually listed the node's live containers; the
	// loader uses it to re-ask the demand question for an in-flight loader's own
	// image, which need not be the image we found orphaned here.
	var demanded operations.DriverDemandCheck
	if decision == operations.DriverConflict {
		// Nothing clears the node's drivers-loaded record when its author's pod
		// is replaced mid-upgrade or the author is deleted, so the record can
		// outlive whoever wrote it. Re-check against who is actually still alive
		// on the node before accepting the conflict as real.
		peers, err := r.nodeFrontendDemands(ctx)
		if err != nil {
			return err
		}
		blocker = operations.BlockingPeer(peers, priority, details.Image)
		if blocker == "" {
			// The record is orphaned by CR-level demand, but a peer mid-deletion may
			// still have a pod holding mounts open, or the pod may belong to a
			// container already excluded above. Catch a pod still running the
			// recorded image before preempting under it. This is best-effort, not a
			// full safety net: it does not see a lenient consumer running a
			// different image than the loaded driver — the backstop for that is the
			// loader's own post-install `weka driver ready` check plus rmmod failing
			// while the module is in use.
			holder, err := r.findLivePodOnImage(ctx, loadedImage)
			if err != nil {
				return err
			}
			if holder != "" {
				msg := fmt.Sprintf(
					"cannot load drivers for image %s on node %s: driver image %s is loaded and pod %s still runs it",
					details.Image, r.node.Name, loadedImage, holder)
				_ = r.RecordEventThrottled(v1.EventTypeWarning, "DriversWaitForConsumer", msg, time.Minute) //nolint:errcheck // event recording is best-effort
				if err := r.updateStatusWaitForDrivers(ctx); err != nil {
					return err
				}
				// Same 30s as the conflict path below: the usual cause is a live backend
				// cluster on the newer image, which does not resolve on its own.
				return lifecycle.NewWaitErrorWithDuration(errors.New(msg), 30*time.Second)
			}
			decision = operations.DriverLoad
			demanded = func(prio int, img string) bool { return operations.PeersDemand(peers, prio, img) }
			// Throttled: the record is only cleared once the load succeeds, so this
			// branch re-fires on every reconcile until then (and indefinitely if the
			// load keeps failing).
			_ = r.RecordEventThrottled(v1.EventTypeNormal, "DriversPreemptStaleRecord", fmt.Sprintf( //nolint:errcheck // event recording is best-effort
				"preempting stale driver record %s on node %s: no live container demands it and no live pod runs it",
				loadedImage, r.node.Name), time.Minute)
		}
	}

	switch decision {
	case operations.DriverSatisfied, operations.DriverDefer:
		// our exact version is loaded, or we are lenient and tolerate what's there
		return nil
	case operations.DriverConflict:
		// strict frontend, but a >=-order incompatible driver is still demanded by
		// a live peer on the node; we cannot run on it and must not churn the
		// shared loader
		msg := fmt.Sprintf(
			"cannot load drivers for image %s: incompatible driver image %s already loaded on node %s, required by %s",
			details.Image, loadedImage, r.node.Name, blocker)
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "DriversVersionConflict", msg, time.Minute) //nolint:errcheck // event recording is best-effort
		if err := r.updateStatusWaitForDrivers(ctx); err != nil {
			return err
		}
		return lifecycle.NewWaitErrorWithDuration(errors.New(msg), time.Second*30)
	}

	// decision == DriverLoad
	if err := r.updateStatusWaitForDrivers(ctx); err != nil {
		return err
	}

	logger.Info("Loading drivers", "image", details.Image, "priority", priority, "preempt", demanded != nil)

	driversLoader := operations.NewLoadDrivers(r.Manager, r.node, *details, r.container.Spec.DriversLoaderImage,
		r.container.Spec.DriversBuildId, r.container.Spec.DriversDistService,
		operations.LoadDriversOptions{Priority: priority, Demanded: demanded})
	err := operations.ExecuteOperation(ctx, driversLoader)
	if err != nil {
		return err
	}
	return nil
}

// nodeFrontendDemands lists the driver-version demands of the other frontend containers
// on this node in desired-state terms: containers are never downgraded, so Spec.Image is
// the conservative per-peer signal.
//
// Frontends suffice because both consumers ask at frontend rank. A lenient caller would
// need every RequiresDrivers() container instead — a backend peer is a real demand at
// backend rank. Peers that have not populated Status.NodeAffinity yet are invisible (the
// node comes from that index); harmless, since EnsureDrivers is gated on HasNodeAffinity.
func (r *containerReconcilerLoop) nodeFrontendDemands(ctx context.Context) ([]operations.DriverDemand, error) {
	containers, err := r.getFrontendWekaContainerOnNode(ctx, r.node.Name)
	if err != nil {
		return nil, err
	}

	demands := make([]operations.DriverDemand, 0, len(containers))
	for i := range containers {
		c := &containers[i]
		// RequiresDrivers() needs no check: it is true for every frontend mode.
		if c.UID == r.container.UID || c.IsMarkedForDeletion() || c.Spec.Image == "" {
			continue
		}
		demands = append(demands, operations.DriverDemand{
			Priority:  driverPriority(c),
			Image:     c.Spec.Image,
			Container: c.Namespace + "/" + c.Name,
		})
	}
	return demands, nil
}

// findLivePodOnImage reports the first non-terminal driver-consuming pod on the
// node still running `image` (as "namespace/name"), or "" if none is found. It is
// the safety guard that keeps preemption from unloading drivers out from under a
// pod that is still using them, even when no live WekaContainer demands that image
// anymore (e.g. the container was deleted but its pod is still terminating).
//
// Only pods whose mode actually consumes drivers count. The rest run the same
// images without holding wekafs mounts, so treating them as consumers would
// re-create the permanent block this whole path exists to remove: a
// drivers-loader pod runs the very image being recorded (GetLoaderImageForNode
// returns the cluster image once weka can copy local driver files), and dist /
// drivers-builder / envoy / telemetry pods are long-lived on the same image.
// Loader-vs-loader ordering belongs to LoadDrivers.HandleExistingLoader, not
// here.
func (r *containerReconcilerLoop) findLivePodOnImage(ctx context.Context, image string) (string, error) {
	pods, err := r.KubeService.GetPods(ctx, kubernetes.GetPodsOptions{
		Node:   r.node.Name,
		Labels: map[string]string{domain.LabelCreatedBy: domain.LabelCreatedByWeka},
	})
	if err != nil {
		return "", err
	}

	for i := range pods {
		pod := &pods[i]
		if r.pod != nil && pod.UID == r.pod.UID {
			continue
		}
		if pod.Status.Phase == v1.PodSucceeded || pod.Status.Phase == v1.PodFailed {
			continue
		}
		// RequiresDrivers() is a pure function of Spec.Mode, so the pod's mode label
		// answers it. An unlabeled pod is kept: under-reporting a consumer is the
		// dangerous direction.
		if mode, ok := pod.Labels[domain.WekaLabelMode]; ok {
			probe := &weka.WekaContainer{Spec: weka.WekaContainerSpec{Mode: mode}}
			if !probe.RequiresDrivers() {
				continue
			}
		}
		wekaPodContainer, err := resources.GetWekaPodContainer(pod)
		if err != nil || wekaPodContainer == nil {
			continue
		}
		if wekaPodContainer.Image == image {
			return pod.Namespace + "/" + pod.Name, nil
		}
	}
	return "", nil
}

// driverPriority ranks a container in the (priority, version) total order that
// selects the node's single loaded driver version. Frontends are strict (need
// their exact version, so they dictate → highest); backend-only containers are
// lenient (tolerate any loaded version → middle); ssdproxy is auxiliary and
// lenient → lowest. Only meaningful for RequiresDrivers() containers.
func driverPriority(c *weka.WekaContainer) int {
	switch {
	case c.IsSSDProxyContainer():
		return 1
	case c.HasFrontend():
		return 3
	default:
		return 2
	}
}

func (r *containerReconcilerLoop) driversLoaded(ctx context.Context) (bool, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "driversLoaded")
	defer logger.End()

	if !r.container.RequiresDrivers() {
		return true, nil
	}

	pod := r.pod
	timeout := 15 * time.Second

	executor, err := podexec.NewExecInPodWithTimeout(r.RestClient, r.Manager.GetConfig(), pod, &timeout)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return false, err
	}
	stdout, stderr, err := executor.ExecNamed(ctx, "CheckDriversLoaded", []string{"bash", "-ce", "cat /tmp/weka-drivers.log"})
	if err != nil {
		// also verify that /opt/weka/k8s-runtime/resources.json exists and is correct
		if meta.IsStatusConditionTrue(r.container.Status.Conditions, condition.CondContainerResourcesWritten) {
			expectedAllocations, err2 := r.getExpectedAllocations(ctx)
			if err2 != nil {
				return false, fmt.Errorf("error getting expected allocations: %v, original err: %v", err2, err)
			}

			err2 = r.verifyResourcesJson(ctx, executor, expectedAllocations)
			if err2 != nil {
				if strings.Contains(err2.Error(), "context deadline exceeded") {
					return false, lifecycle.NewWaitErrorWithDuration(err2, time.Second*10)
				}

				err2 = fmt.Errorf("error checking resources.json: %v, original err: %v", err2, err)

				logger.Error(err2, "resources.json is incorrect, re-writing it")

				err3 := r.WriteResources(ctx)
				if err3 != nil {
					err3 = fmt.Errorf("error writing resources.json: %v, prev. error %v", err3, err2)
					return false, err3
				}
			}

		}
		return false, fmt.Errorf("error checking drivers loaded: %v, %s", err, stderr.String())
	}

	missingDriverName := strings.TrimSpace(stdout.String())

	if missingDriverName == "" {
		logger.InfoWithStatus(codes.Ok, "Drivers already loaded")
		return true, nil
	}

	logger.Info("Driver not loaded", "missing_driver", missingDriverName)
	return false, nil
}

type BuiltDriversResult struct {
	WekaVersion           string `json:"weka_version"`
	KernelSignature       string `json:"kernel_signature"`
	WekaPackNotSupported  bool   `json:"weka_pack_not_supported"`
	NoWekaDriversHandling bool   `json:"no_weka_drivers_handling"`
	Err                   string `json:"err"`
}

func (r *containerReconcilerLoop) UploadBuiltDrivers(ctx context.Context) error {
	targetDistContainer, err := r.getTargetContainer(ctx)
	if err != nil {
		return err
	}

	complete := func() error {
		r.container.Status.Status = weka.Completed
		return r.Status().Update(ctx, r.container)
	}

	// TODO: This is not a best solution, to download version, but, usable.
	// Should replace this with ad-hocy downloader container, that will use newer version(as the one who built), to download using shared storage

	if r.pod == nil {
		return errors.New("pod not found")
	}

	executor, err := r.ExecService.GetExecutor(ctx, targetDistContainer)
	if err != nil {
		return err
	}

	builderIp := r.pod.Status.PodIP
	builderPort := r.container.GetPort()

	if builderIp == "" {
		return errors.New("Builder IP is not set")
	}

	results := &BuiltDriversResult{}
	err = json.Unmarshal([]byte(*r.container.Status.ExecutionResult), results)
	if err != nil {
		return err
	}

	if results.NoWekaDriversHandling {
		// for legacy drivers handling, we don't have support for weka driver command
		// copy everything from builder's /opt/weka/dist/drivers to targetDistcontainer's /opt/weka/dist/drivers
		cmd := fmt.Sprintf("cd /opt/weka/dist/drivers/ && wget -r -nH --cut-dirs=3 --no-parent --reject=\"index.html*\" http://%s:%d/dist/v1/drivers/", builderIp, builderPort)
		stdout, stderr, execErr := executor.ExecNamed(ctx, "CopyDrivers",
			[]string{"bash", "-ce", cmd},
		)
		if execErr != nil {
			return fmt.Errorf("failed to run command: %s, error: %s, stdout: %s, stderr: %s", cmd, execErr, stdout.String(), stderr.String())
		}
		return complete()
	}

	endpoint := fmt.Sprintf("http://%s:%d", builderIp, builderPort)

	// if weka pack is not supported, we don't need to download it
	if !results.WekaPackNotSupported {
		stdout, stderr, execErr := executor.ExecNamed(ctx, "DownloadVersion",
			[]string{"bash", "-ce",
				"weka version get --driver-only " + results.WekaVersion + " --from " + endpoint,
			},
		)
		if execErr != nil {
			return errors.Wrap(execErr, stderr.String()+stdout.String())
		}
	}

	downloadCmd := "weka driver download --without-agent --version " + results.WekaVersion + " --from " + endpoint
	if !results.WekaPackNotSupported {
		downloadCmd += " --kernel-signature " + results.KernelSignature
	}

	stdout, stderr, err := executor.ExecNamed(ctx, "DownloadDrivers",
		[]string{"bash", "-ce", downloadCmd},
	)
	if err != nil {
		return errors.Wrap(err, stderr.String()+stdout.String())
	}

	if results.WekaPackNotSupported {
		url := fmt.Sprintf("%s/dist/v1/drivers/%s-%s.tar.gz.sha256", endpoint, results.WekaVersion, results.KernelSignature)
		cmd := "cd /opt/weka/dist/drivers/ && curl -kO " + url
		stdout, stderr, err = executor.ExecNamed(ctx, "Copy sha256 file",
			[]string{"bash", "-ce", cmd},
		)
		if err != nil {
			return fmt.Errorf("failed to run command: %s, error: %s, stdout: %s, stderr: %s", cmd, err, stdout.String(), stderr.String())
		}
	}

	return complete()
}

func (r *containerReconcilerLoop) getTargetContainer(ctx context.Context) (*weka.WekaContainer, error) {
	target := r.container.Spec.UploadResultsTo
	if target == "" {
		return nil, errors.New("uploadResultsTo is not set")
	}

	targetDistContainer := &weka.WekaContainer{}
	// assuming same namespace
	err := r.Get(ctx, client.ObjectKey{Name: target, Namespace: r.container.Namespace}, targetDistContainer)
	if err != nil {
		return nil, errors.Wrap(err, "error getting target dist container")
	}

	return targetDistContainer, nil
}

func (r *containerReconcilerLoop) updateDriversBuilderStatus(ctx context.Context) error {
	return r.updateContainerStatusIfNotEquals(ctx, weka.Building)
}

// check if we actually can load drivers from dist service
// trigger re-build + re-upload if not
func (r *containerReconcilerLoop) uploadedDriversPeriodicCheck(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	if !r.container.IsDriversBuilder() {
		return nil
	}

	if r.container.Status.ExecutionResult == nil {
		logger.Debug("No execution result, skipping")
		return nil
	}

	results := &BuiltDriversResult{}
	err := json.Unmarshal([]byte(*r.container.Status.ExecutionResult), results)
	if err != nil {
		return err
	}

	logger.Info("Try loading drivers", "weka_version", results.WekaVersion, "kernel_signature", results.KernelSignature)

	targetDistContainer, err := r.getTargetContainer(ctx)
	if err != nil {
		return err
	}

	executor, err := r.ExecService.GetExecutor(ctx, targetDistContainer)
	if err != nil {
		return err
	}

	// assuming `weka driver pack` is supported
	downloadCmd := fmt.Sprintf(
		"weka driver download --without-agent --version %s --kernel-signature %s",
		results.WekaVersion, results.KernelSignature,
	)

	stdout, stderr, err := executor.ExecNamed(ctx, "DownloadDrivers",
		[]string{"bash", "-ce", downloadCmd},
	)
	if err != nil {
		err = fmt.Errorf("error downloading drivers: %w, stderr: %s", err, stderr.String())
		logger.Debug(err.Error())

		if strings.Contains(stderr.String(), "Failed to download the drivers") || strings.Contains(stderr.String(), "Version missing") {
			msg := "Cannot load drivers, trigger re-build and re-upload"
			logger.Info(msg)

			_ = r.RecordEvent("", "DriversRebuild", msg) //nolint:errcheck // error return value intentionally not checked

			if clearErr := r.clearStatus(ctx); clearErr != nil {
				return fmt.Errorf("error clearing builder results: %w", clearErr)
			}
		}
		return err
	}

	logger.Debug("Drivers loaded successfully", "stdout", stdout.String())
	return nil
}
