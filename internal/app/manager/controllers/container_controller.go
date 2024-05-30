package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/kr/pretty"
	"github.com/pkg/errors"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/container"
	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/util"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/strings/slices"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const bootScriptConfigName = "weka-boot-scripts"

func NewContainerController(mgr ctrl.Manager) *ContainerController {
	config := mgr.GetConfig()
	client := mgr.GetClient()
	return &ContainerController{
		Client: client,
		Scheme: mgr.GetScheme(),
		Logger: mgr.GetLogger().WithName("controllers").WithName("Container"),

		CrdManager:  services.NewCrdManager(mgr),
		KubeService: services.NewKubeService(client),
		ExecService: services.NewExecService(config),
	}
}

type ContainerController struct {
	client.Client
	Scheme *runtime.Scheme
	Logger logr.Logger

	CrdManager  services.CrdManager
	KubeService services.KubeService
	ExecService services.ExecService
}

//+kubebuilder:rbac:groups=weka.weka.io,resources=wekaclusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekaclusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekaclusters/finalizers,verbs=update
//+kubebuilder:rbac:groups=weka.weka.io,resources=tombstones,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=tombstones/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=tombstones/finalizers,verbs=update
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekaclients,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekaclients/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekaclients/finalizers,verbs=update
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekacontainers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekacontainers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekacontainers/finalizers,verbs=update
//+kubebuilder:rbac:groups=weka.weka.io,resources=driveclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=driveclaims/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=driveclaims/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods/exec,verbs=create
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;update;create
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;update;create
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;update
//+kubebuilder:rbac:groups="batch",resources=jobs,verbs=get;list;update;create

// Reconcile reconciles a WekaContainer resource
func (r *ContainerController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "WekaContainerReconcile", "namespace", req.Namespace, "container_name", req.Name)
	defer end()

	state := &container.ContainerState{
		ReconciliationState: lifecycle.ReconciliationState[*wekav1alpha1.WekaContainer]{
			Request:    req,
			Subject:    &wekav1alpha1.WekaContainer{},
			Conditions: &[]metav1.Condition{},
		},
	}
	setupSteps := &lifecycle.ReconciliationSteps[*wekav1alpha1.WekaContainer]{
		Reconciler: r.Client,
		State:      &state.ReconciliationState,
		Steps: []lifecycle.Step{
			{
				Condition:  "RefreshContainer",
				Predicates: []lifecycle.PredicateFunc{},
				Reconcile:  state.RefreshContainer(r.CrdManager),
			},
			{
				Condition: "EnsureFinalizer",
				Reconcile: state.EnsureFinalizer(r.Client, WekaFinalizer),
			},
			{
				Condition:             "DeleteContainer",
				SkipOwnConditionCheck: true,
				Reconcile:             state.DeleteContainer(r.Client, r.CrdManager, WekaFinalizer),
			},
		},
	}
	if err := setupSteps.Reconcile(ctx); err != nil {
		logger.Error(err, "Error reconciling container")
		return ctrl.Result{}, err
	}

	container := setupSteps.State.Subject

	logger.SetAttributes(
		attribute.String("container", container.Name),
		attribute.String("namespace", container.Namespace),
		attribute.String("mode", container.Spec.Mode),
		attribute.String("management_ip", container.Status.ManagementIP),
		attribute.String("uuid", string(container.GetUID())),
	)
	if err := r.initState(ctx, container); err != nil {
		return ctrl.Result{}, err
	}
	logger.SetPhase("INIT_STATE")

	steps := &lifecycle.ReconciliationSteps[*wekav1alpha1.WekaContainer]{
		Reconciler: r.Client,
		State:      setupSteps.State,
		Steps: []lifecycle.Step{
			{
				Condition: "EnsureBootConfigMap",
				Reconcile: state.EnsureBootConfigMap(r.Client, bootScriptConfigName),
			},
		},
	}
	if err := steps.Reconcile(ctx); err != nil {
		logger.Error(err, "Error reconciling container")
		return ctrl.Result{}, err
	}

	desiredPod, err := resources.NewContainerFactory(container).Create(ctx)
	if err != nil {
		logger.Error(err, "Error creating pod spec")
		return ctrl.Result{}, errors.Wrap(err, "Failed to create pod spec")
	}

	if err := ctrl.SetControllerReference(container, desiredPod, r.Scheme); err != nil {
		logger.Error(err, "Error setting controller reference")
		return ctrl.Result{}, pretty.Errorf("Error setting controller reference", err, desiredPod)
	}

	actualPod, err := r.CrdManager.RefreshPod(ctx, container)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.SetPhase("CREATING_POD")
			if err := r.Create(ctx, desiredPod); err != nil {
				logger.Error(err, "Error creating pod", "pod_name", container.Name)
				return ctrl.Result{}, pretty.Errorf("Error creating pod", err, desiredPod)
			}
			logger.SetPhase("POD_CREATED")
			return ctrl.Result{Requeue: true}, nil
		} else {
			logger.SetPhase("POD_REFRESH_ERROR")
			return ctrl.Result{}, errors.Wrap(err, "Failed to refresh pod")
		}
	} else {
		logger.SetPhase("POD_ALREADY_EXISTS")
		if actualPod.Status.Phase == v1.PodPending {
			// Do we actually have a node that is assigned to it?
			err := r.CleanupIfNeeded(ctx, container, actualPod)
			if err != nil {
				return ctrl.Result{}, err
			}
		}

		if actualPod.Status.Phase != v1.PodRunning {
			logger.SetPhase("POD_NOT_RUNNING")
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 3}, nil
		}
	}

	if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondEnsureDrivers) &&
		!container.IsServiceContainer() {
		err := r.reconcileDriversStatus(ctx, container, actualPod)
		if err != nil {
			if strings.Contains(err.Error(), "No such file or directory") {
				logger.SetPhase("DRIVERS_NOT_READY")
			}
			logger.Error(err, "Error reconciling drivers status", "name", container.Name)
			return ctrl.Result{Requeue: true, RequeueAfter: 3 * time.Second}, nil
		}
		meta.SetStatusCondition(&container.Status.Conditions, metav1.Condition{
			Type:   condition.CondEnsureDrivers,
			Status: metav1.ConditionTrue, Reason: "Success", Message: "Drivers are ensured",
		})
		err = r.Status().Update(ctx, container)
		if err != nil {
			logger.Error(err, "Error updating status for drivers ensured")
			return ctrl.Result{}, err
		}
		logger.SetPhase("DRIVERS_ENSURED")
	} else {
		logger.SetPhase("DRIVERS_ALREADY_ENSURED")
	}

	if !container.IsServiceContainer() {
		result, err := r.reconcileManagementIP(ctx, container, actualPod)
		if err != nil {
			logger.Error(err, "Error reconciling management IP", "name", container.Name)
			return ctrl.Result{}, err
		}
		if result.Requeue {
			return result, nil
		}
		logger.SetAttributes(
			attribute.String("management_ip", container.Status.ManagementIP),
		)

		// pre-clusterize
		if !slices.Contains([]string{wekav1alpha1.WekaContainerModeDriversLoader}, container.Spec.Mode) {
			result, err = r.reconcileWekaLocalStatus(ctx, container, actualPod)
			if err != nil {
				logger.Error(err, "Error reconciling status", "name", container.Name)
				return ctrl.Result{}, err
			}
			if result.Requeue {
				return result, nil
			}
		}
	}
	// only for drivers-loader container: check if drivers loaded
	if container.Spec.Mode == wekav1alpha1.WekaContainerModeDriversLoader {
		err := r.checkIfLoaderFinished(ctx, actualPod)
		if err != nil {
			return ctrl.Result{RequeueAfter: time.Second * 3, Requeue: true}, err
		} else {
			// if drivers loaded we can delete this weka container
			err := r.Delete(ctx, container)
			if err != nil {
				return ctrl.Result{}, err
			}
			logger.SetPhase("DELETING_DRIVER_LOADER")
		}
		logger.SetPhase("DRIVERS_LOADED")
	}

	if container.IsServiceContainer() {
		return ctrl.Result{}, nil
	}

	// check if clusterize is needed. for standalone containers without owner references, skip
	ownerRefs := container.GetObjectMeta().GetOwnerReferences()
	if len(ownerRefs) == 0 {
		logger.Info("Owner references not set")
		logger.InfoWithStatus(codes.Ok, "Container is ready")
		logger.SetPhase("CONTAINER_IS_READY")
		return ctrl.Result{}, nil
	}

	// post-clusterize
	if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondJoinedCluster) {
		retry, err := r.reconcileClusterStatus(ctx, container, actualPod)
		if retry || err != nil {
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 3}, err
		}
		meta.SetStatusCondition(&container.Status.Conditions, metav1.Condition{
			Type:   condition.CondJoinedCluster,
			Status: metav1.ConditionTrue, Reason: "Success", Message: fmt.Sprintf("Joined cluster %s", container.Status.ClusterID),
		})
		err = r.Status().Update(ctx, container)
		if err != nil {
			r.Logger.Error(err, "Error updating status")
			return ctrl.Result{}, err
		}
		logger.SetPhase("CLUSTER_FORMED")
	} else {
		logger.SetPhase("CLUSTER_ALREADY_FORMED")
	}

	container, err = r.CrdManager.RefreshContainer(ctx, req)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "refreshContainer")
	}
	if container.Spec.Mode == "drive" &&
		!meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondDrivesAdded) &&
		container.Spec.NumDrives > 0 {

		retry, err := r.ensureDrives(ctx, container, actualPod)
		if retry || err != nil {
			return ctrl.Result{Requeue: true, RequeueAfter: 3 * time.Second}, err
		}
		meta.SetStatusCondition(&container.Status.Conditions, metav1.Condition{
			Type:   condition.CondDrivesAdded,
			Status: metav1.ConditionTrue, Reason: "Success", Message: fmt.Sprintf("Added %d drives", container.Spec.NumDrives),
		})
		err = r.Status().Update(ctx, container)
		if err != nil {
			r.Logger.Error(err, "Error updating status")
			return ctrl.Result{}, err
		}
		logger.SetPhase("DRIVES_ADDED")
	} else {
		logger.SetPhase("DRIVES_ALREADY_ADDED")
	}

	if container.Spec.Mode == wekav1alpha1.WekaContainerModeS3 && container.Spec.JoinIps != nil {
		if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondJoinedS3Cluster) {
			wekaService := services.NewWekaService(r.ExecService, container)
			err := wekaService.JoinS3Cluster(ctx, *container.Status.ClusterContainerID)
			if err != nil {
				return ctrl.Result{}, err
			}
			meta.SetStatusCondition(&container.Status.Conditions, metav1.Condition{
				Type:   condition.CondJoinedS3Cluster,
				Status: metav1.ConditionTrue, Reason: "Success", Message: "Joined S3 cluster",
			})
			err = r.Status().Update(ctx, container)
			if err != nil {
				r.Logger.Error(err, "Error updating status")
				return ctrl.Result{}, err
			}
		}
	}

	if container.Spec.Image != container.Status.LastAppliedImage {
		pod, err := r.CrdManager.RefreshPod(ctx, container)
		if err != nil {
			return ctrl.Result{}, errors.Wrap(err, "refreshPod")
		}
		var wekaPodContainer v1.Container
		found := false
		for _, podContainer := range pod.Spec.Containers {
			if podContainer.Name == "weka-container" {
				wekaPodContainer = podContainer
				found = true
			}
		}
		if !found {
			return ctrl.Result{}, errors.New("weka-container not found in pod")
		}

		if wekaPodContainer.Image != container.Spec.Image {
			// delete pod
			err := r.Delete(ctx, pod)
			if err != nil {
				return ctrl.Result{}, errors.Wrap(err, "Delete pod")
			}
			return ctrl.Result{Requeue: true}, nil
		}

		if pod.GetDeletionTimestamp() != nil {
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second}, nil
		}

		if pod.Status.Phase != v1.PodRunning {
			return ctrl.Result{Requeue: true, RequeueAfter: 3 * time.Second}, nil
		}

		container.Status.LastAppliedImage = container.Spec.Image
		err = r.Status().Update(ctx, container)
		if err != nil {
			return ctrl.Result{}, errors.Wrap(err, "Update container status")
		}
	}

	logger.SetPhase("CONTAINER_IS_READY")
	return ctrl.Result{}, nil
}

func (r *ContainerController) reconcileManagementIP(ctx context.Context, container *wekav1alpha1.WekaContainer, pod *v1.Pod) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "reconcileManagementIP")
	defer end()

	if container.Status.ManagementIP != "" {
		return ctrl.Result{}, nil
	}
	executor, err := util.NewExecInPod(pod)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return ctrl.Result{}, err
	}

	var getIpCmd string
	if container.Spec.Network.EthDevice != "" {
		getIpCmd = fmt.Sprintf("ip addr show dev %s | grep 'inet ' | awk '{print $2}' | cut -d/ -f1", container.Spec.Network.EthDevice)
	} else {
		getIpCmd = fmt.Sprintf("ip route show default | grep src | awk '/default/ {print $9}' | head -n1")
	}

	stdout, stderr, err := executor.ExecNamed(ctx, "GetManagementIpAddress", []string{"bash", "-ce", getIpCmd})
	if err != nil {
		logger.Error(err, "Error executing command", "stderr", stderr.String())
		return ctrl.Result{}, err
	}
	ipAddress := strings.TrimSpace(stdout.String())
	logger.WithValues("management_ip", ipAddress).Info("Got management IP")
	if container.Status.ManagementIP != ipAddress {
		container.Status.ManagementIP = ipAddress
		if err := r.Status().Update(ctx, container); err != nil {
			logger.Error(err, "Error updating status")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{}, nil
}

func (r *ContainerController) reconcileWekaLocalStatus(ctx context.Context, container *wekav1alpha1.WekaContainer, pod *v1.Pod) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "reconcileWekaLocalStatus")
	defer end()

	if slices.Contains([]string{wekav1alpha1.WekaContainerModeDriversLoader}, container.Spec.Mode) {
		return ctrl.Result{}, nil
	}

	executor, err := util.NewExecInPod(pod)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return ctrl.Result{}, err
	}

	statusCommand := fmt.Sprintf("weka local ps -J")
	stdout, stderr, err := executor.ExecNamed(ctx, "WekaLocalPs", []string{"bash", "-ce", statusCommand})
	if err != nil {
		logger.Error(err, "Error executing command", "command", statusCommand, "stderr", stderr.String())
		return ctrl.Result{}, err
	}
	response := []resources.WekaLocalPs{}
	err = json.Unmarshal(stdout.Bytes(), &response)
	if err != nil {
		logger.Error(err, "Error unmarshalling response", "stdout", stdout.String())
		return ctrl.Result{}, err
	}

	if len(response) == 0 {
		logger.InfoWithStatus(codes.Error, fmt.Sprintf("Expected at least one container to be present, none found"))
		return ctrl.Result{}, errors.New("expected exactly one container to be present")
	}

	found := false
	for _, c := range response {
		if c.Name == container.Spec.WekaContainerName {
			found = true
			break
		}
	}

	if !found {
		logger.InfoWithStatus(codes.Error, "Weka container not found", "name", response, "expected_name", container.Spec.WekaContainerName)
		return ctrl.Result{Requeue: true}, nil
	}

	status := response[0].RunStatus
	if container.Status.Status != status {
		logger.Info("Updating status", "from", container.Status.Status, "to", status)
		container.Status.Status = status
		if err := r.Status().Update(ctx, container); err != nil {
			logger.Error(err, "Error updating status")
			return ctrl.Result{}, err
		}
		logger.WithValues("status", status).Info("Status updated")
		return ctrl.Result{Requeue: true}, nil
	}
	logger.SetStatus(codes.Ok, "Status reconciled")
	return ctrl.Result{}, nil
}

func (r *ContainerController) updatePod(ctx context.Context, pod *v1.Pod) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "updatePod")
	defer end()

	if err := r.Update(ctx, pod); err != nil {
		logger.Error(err, "Error updating pod", "pod", pod)
		return err
	}
	return nil
}

func (r *ContainerController) SetupWithManager(mgr ctrl.Manager, wrappedReconcile reconcile.Reconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.WekaContainer{}).
		Owns(&v1.Pod{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 10}).
		Complete(wrappedReconcile)
}

func (r *ContainerController) reconcileClusterStatus(ctx context.Context, container *wekav1alpha1.WekaContainer, pod *v1.Pod) (bool, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "reconcileClusterStatus")
	defer end()

	logger.Info("Reconciling cluster status")
	executor, err := util.NewExecInPod(pod)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return true, nil
	}
	logger.Debug("Querying weka local status")

	cmd := "weka local status -J"
	if container.Spec.JoinIps != nil {
		cmd = fmt.Sprintf("wekaauthcli local status -J")
	}

	stdout, _, err := executor.ExecNamed(ctx, "WekaLocalStatus", []string{"bash", "-ce", cmd})
	if err != nil {
		logger.Error(err, "Error querying weka local status")
		return true, err
	}
	logger.Debug("Parsing weka local status")
	response := resources.WekaLocalStatusResponse{}
	err = json.Unmarshal(stdout.Bytes(), &response)
	if err != nil {
		logger.Error(err, "Error parsing weka local status")
		return true, err
	}

	if _, ok := response[container.Spec.WekaContainerName]; !ok {
		logger.InfoWithStatus(codes.Unset, "Container not found")
		return true, errors.New("container not found")
	}
	if len(response[container.Spec.WekaContainerName].Slots) == 0 {
		logger.InfoWithStatus(codes.Unset, "Slots not found")
		return true, errors.New("slots not found")
	}

	if !container.IsBackend() {
		return false, nil // TODO: clients do not update clusterId, need better way to validate if client indeed joined and can serve IOs
	}

	clusterId := response[container.Spec.WekaContainerName].Slots[0].ClusterID
	if clusterId == "" || clusterId == "00000000-0000-0000-0000-000000000000" {
		logger.InfoWithStatus(codes.Unset, "Cluster not ready")
		return true, nil
	}

	container.Status.ClusterID = clusterId
	logger.InfoWithStatus(codes.Ok, "Cluster created and its GUID updated in WekaContainer status")
	if err := r.Status().Update(ctx, container); err != nil {
		return true, err
	}
	return false, nil
}

func (r *ContainerController) ensureDrives(ctx context.Context, container *wekav1alpha1.WekaContainer, pod *v1.Pod) (bool, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureDrives")
	defer end()

	if container.Status.ClusterContainerID == nil {
		logger.InfoWithStatus(codes.Error, "ClusterContainerID not set, cannot ensure drives")
		return true, nil
	}
	executor, err := util.NewExecInPod(pod)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return true, err
	}

	numAdded := 0
	driveCursor := 0
DRIVES:
	for i := 0; i < container.Spec.NumDrives; i++ {
		for driveCursor < len(container.Spec.PotentialDrives) {
			l := logger.WithValues("drive_name", container.Spec.PotentialDrives[driveCursor])
			l.Info("Attempting to configure drive")
			drive := container.Spec.PotentialDrives[driveCursor]
			drive = r.discoverDrive(ctx, executor, drive)
			if drive == "" {
				l.Info("Drive not found, moving to next")
				driveCursor++
				continue
			}
			driveSignTarget := getSignatureDevice(drive)

			l.Info("Verifying drive signature")
			cmd := fmt.Sprintf("hexdump -v -e '1/1 \"%%.2x\"' -s 8 -n 16 %s", driveSignTarget)
			stdout, stderr, err := executor.ExecNamed(ctx, "GetPartitionSignature", []string{"bash", "-ce", cmd})
			if err != nil {
				if strings.Contains(stderr.String(), "No such file or directory") { // it can be actual missing device
					logger.Debug("Failed to read drive signature, or partition does not exist", "drive", drive)
					if strings.HasPrefix(container.Spec.PotentialDrives[driveCursor], "aws_") ||
						strings.HasPrefix(container.Spec.PotentialDrives[driveCursor], "/dev/oracleoci") {
						l.Info("Drive is not presigned, assuming a new instance")
						if err := r.initSignCloudDrives(ctx, executor, drive); err != nil {
							l.Error(err, "Failed to sign cloud drive, continuing to next drive")
							// no return or continue on purpose, it is only opportunistic presigning while moving to next drive regardless
						}
					}
					logger.Info("Drive does not exist or not pre-signed, moving to next", "drive", drive)
					driveCursor++
					continue
				} else {
					return true, errors.Wrap(err, stderr.String()+"\n"+stdout.String())
				}
			}

			// Validate that disk is weka-signed
			l.Info("Checking if the partition is of type Weka")
			presigned, err := r.isDrivePresigned(ctx, executor, drive)
			if !presigned {
				l.Info("Partition is not Weka or not presigned, moving to next")
				driveCursor++
				continue
			}

			if stdout.String() != "90f0090f90f0090f90f0090f90f0090f" {
				l.Info("Drive has Weka signature on it, verifying ownership")
				exists, err := r.isExistingCluster(ctx, stdout.String())
				if err != nil {
					return true, err
				}
				if exists {
					l.WithValues("cluster_guid", stdout.String()).Info("Drive belongs to a different live cluster")
					driveCursor++
					continue
				} else {
					l.WithValues("cluster_guid", stdout.String()).Info("Drive belongs to non-existing cluster, resigning")
					err2 := r.claimDrive(ctx, container, executor, drive)
					if err2 != nil {
						l.Error(err2, "Error claiming drive for resigning")
						driveCursor++
						continue
					}
					err2 = r.reSignDrive(ctx, executor, drive) // This changes UUID, effectively making claim obsolete
					if err2 != nil {
						l.Error(err2, "Error resigning drive", "drive", drive)
						driveCursor++
						continue
					}
				}
			}

			l.Info("Claiming drive")

			err = r.claimDrive(ctx, container, executor, drive)
			if err != nil {
				l.WithValues("container", container.Name).Error(err, "Error claiming drive")
				driveCursor++
				continue
			}
			l.Info("Adding drive into system")
			// TODO: We need to login here. Maybe handle it on wekaauthcli level?
			wekaCmd := "weka"
			if container.Spec.JoinIps != nil {
				wekaCmd = "wekaauthcli"
			}
			cmd = fmt.Sprintf("%s cluster drive add %d %s", wekaCmd, *container.Status.ClusterContainerID, drive)
			_, stderr, err = executor.ExecNamed(ctx, "WekaClusterDriveAdd", []string{"bash", "-ce", cmd})
			if err != nil {
				l.WithValues("stderr", stderr.String()).Error(err, "Error adding drive into system")
				return true, errors.Wrap(err, stderr.String())
			} else {
				l.Info("Drive added into system")
				logger.Info("Drive added into system", "drive", drive)
			}
			numAdded++
			driveCursor++
			continue DRIVES
		}
		return true, errors.New(fmt.Sprintf("Could not allocate drive %d", i))
	}
	logger.InfoWithStatus(codes.Ok, "Drives added")
	return false, nil
}

func getSignatureDevice(drive string) string {
	driveSignTarget := fmt.Sprintf("%s1", drive)
	if strings.Contains(drive, "/dev/disk/by-path/pci-") {
		return fmt.Sprintf("%s-part1", drive)
	}
	if strings.Contains(drive, "nvme") {
		return fmt.Sprintf("%sp1", drive)
	}
	return driveSignTarget
}

func (r *ContainerController) reSignDrive(ctx context.Context, executor util.Exec, drive string) error {
	cmd := fmt.Sprintf("weka local exec -- /weka/tools/weka_sign_drive --force %s", drive)
	_, stderr, err := executor.ExecNamed(ctx, "WekaSignDrive", []string{"bash", "-ce", cmd})
	if err != nil {
		r.Logger.Error(err, "Error signing drive", "drive", drive, "stderr", stderr.String())
	}
	return err
}

func (r *ContainerController) isExistingCluster(ctx context.Context, s string) (bool, error) {
	// TODO: Query by status?
	// TODO: Cache?
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "isExistingCluster")
	defer end()

	logger.WithValues("cluster_guid", s).Info("Verifying for existing cluster")
	clusterList := wekav1alpha1.WekaClusterList{}
	err := r.List(ctx, &clusterList)
	if err != nil {
		logger.Error(err, "Error listing clusters")
		return false, err
	}
	for _, cluster := range clusterList.Items {
		// strip `-` from saved cluster name
		stripped := strings.ReplaceAll(cluster.Status.ClusterID, "-", "")
		if stripped == s {
			logger.InfoWithStatus(codes.Ok, "Cluster found")
			return true, nil
		}
	}
	logger.InfoWithStatus(codes.Ok, "Cluster not found")
	return false, nil
}

func (r *ContainerController) validateNotMounted(ctx context.Context, executor util.Exec, drive string) (bool, error) {
	return false, nil
}

func (r *ContainerController) isDrivePresigned(ctx context.Context, executor util.Exec, drive string) (bool, error) {
	stdout, stderr, err := executor.ExecNamed(ctx, "CheckDriveIsPresigned", []string{"bash", "-ce", "blkid -s PART_ENTRY_TYPE -o value -p " + getSignatureDevice(drive)})
	if err != nil {
		r.Logger.Error(err, "Error checking if drive is presigned", "drive", drive, "stderr", stderr.String(), "stdout", stdout.String())
		return false, errors.Wrap(err, stderr.String())
	}
	const WEKA_SIGNATURE = "993ec906-b4e2-11e7-a205-a0a8cd3ea1de"
	return strings.TrimSpace(stdout.String()) == WEKA_SIGNATURE, nil
}

func (r *ContainerController) claimDrive(ctx context.Context, container *wekav1alpha1.WekaContainer, executor util.Exec, drive string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "claimDrive", "drive", drive)
	defer end()

	logger.Info("Claiming drive")
	driveUuid, err := r.getDriveUUID(ctx, executor, drive)
	if err != nil {
		logger.Error(err, "Error getting drive UUID")
		return err
	}
	logger.SetValues("drive_uuid", driveUuid)
	logger.Info("Claimed drive by UUID")

	claim := wekav1alpha1.DriveClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: container.Namespace,
			Name:      fmt.Sprintf("%s", driveUuid),
		},
		Spec:   wekav1alpha1.DriveClaimSpec{},
		Status: wekav1alpha1.DriveClaimStatus{},
	}

	err = ctrl.SetControllerReference(container, &claim, r.Scheme)
	if err != nil {
		logger.SetError(err, "Error setting owner reference")
		return err
	}
	logger.Info("Drive was set with owner, creating drive claim")

	err = r.Create(ctx, &claim)
	if err != nil {
		// get eixsting
		existingClaim := wekav1alpha1.DriveClaim{}
		err = r.Get(ctx, client.ObjectKey{Namespace: container.Namespace, Name: fmt.Sprintf("%s", driveUuid)}, &existingClaim)
		if err != nil {
			logger.SetError(err, "Error getting existing claim")
			return err
		}
		if existingClaim.OwnerReferences[0].UID != container.UID {
			err = errors.New("drive already claimed by another container")
			logger.SetError(err, "drive already claimed")
		}
		return nil
	}
	return nil
}

func (r *ContainerController) getDriveUUID(ctx context.Context, executor util.Exec, drive string) (string, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getDriveUUID")
	defer end()

	cmd := fmt.Sprintf("blkid -o value -s PARTUUID %s", getSignatureDevice(drive))
	stdout, stderr, err := executor.ExecNamed(ctx, "GetDriveUUID", []string{"bash", "-ce", cmd})
	if err != nil {
		logger.WithValues("stderr", stderr.String()).Error(err, "Error getting drive UUID")
		return "", errors.Wrap(err, stderr.String())
	}
	serial := strings.TrimSpace(stdout.String())
	if serial == "" {
		logger.Error(err, "UUID not found for drive")
		return "", errors.New("uuid not found")
	}
	logger.InfoWithStatus(codes.Ok, "UUID found for drive")
	return serial, nil
}

func (r *ContainerController) initState(ctx context.Context, container *wekav1alpha1.WekaContainer) error {
	if container.Status.Conditions == nil {
		container.Status.Conditions = []metav1.Condition{}
	}

	// All container types are being set to False on init
	// This includes types not listed here (beyond dist and drivers-loader)
	// TODO: Is this expected?
	changes := false
	if !container.DriversReady() && container.SupportsEnsureDriversCondition() {
		changes = true
		meta.SetStatusCondition(&container.Status.Conditions,
			metav1.Condition{Type: condition.CondEnsureDrivers, Status: metav1.ConditionFalse, Message: "Init", Reason: "Init"},
		)
	}

	if container.Status.LastAppliedImage == "" {
		container.Status.LastAppliedImage = container.Spec.Image
		changes = true
	}

	if changes {
		err := r.Status().Update(ctx, container)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *ContainerController) reconcileDriversStatus(ctx context.Context, container *wekav1alpha1.WekaContainer, pod *v1.Pod) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "reconcileDriversStatus")
	defer end()

	if container.IsServiceContainer() {
		return nil
	}

	executor, err := util.NewExecInPod(pod)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return err
	}
	stdout, stderr, err := executor.ExecNamed(ctx, "CheckDriversLoaded", []string{"bash", "-ce", "cat /tmp/weka-drivers.log"})
	if err != nil {
		logger.WithValues("stderr", stderr.String()).Error(err, "Error executing command")
		return errors.Wrap(err, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		logger.InfoWithStatus(codes.Ok, "Drivers already loaded")
		return nil
	}

	if container.Spec.DriversDistService != "" {
		logger.Info("Drivers not loaded, ensuring drivers dist service")
		err2 := r.ensureDriversLoader(ctx, container)
		if err2 != nil {
			r.Logger.Error(err2, "Error ensuring drivers loader", "container", container)
		}
	}

	return errors.New("Drivers not loaded")
}

func (r *ContainerController) ensureDriversLoader(ctx context.Context, container *wekav1alpha1.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureDriversLoader", "container", container.Name)
	defer end()

	pod, err := r.CrdManager.RefreshPod(ctx, container)
	if err != nil {
		logger.Error(err, "Error refreshing pod")
		return err
	}
	// namespace := pod.Namespace
	namespace, err := util.GetPodNamespace()
	if err != nil {
		logger.Error(err, "GetPodNamespace")
		return err
	}
	loaderContainer := &wekav1alpha1.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "weka-drivers-loader-" + pod.Spec.NodeName,
			Namespace: namespace,
		},
		Spec: wekav1alpha1.WekaContainerSpec{
			Image:               container.Spec.Image,
			Mode:                wekav1alpha1.WekaContainerModeDriversLoader,
			ImagePullSecret:     container.Spec.ImagePullSecret,
			Hugepages:           0,
			NodeAffinity:        container.Spec.NodeAffinity,
			DriversDistService:  container.Spec.DriversDistService,
			TracesConfiguration: container.Spec.TracesConfiguration,
		},
	}

	found := &wekav1alpha1.WekaContainer{}
	err = r.Get(ctx, client.ObjectKey{Name: loaderContainer.Name, Namespace: loaderContainer.ObjectMeta.Namespace}, found)
	l := logger.WithValues("container", loaderContainer.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			l.Info("Creating drivers loader pod", "node_name", pod.Spec.NodeName, "namespace", loaderContainer.Namespace)
			err = r.Create(ctx, loaderContainer)
			if err != nil {
				l.Error(err, "Error creating drivers loader pod")
				return err
			}
		}
	}
	if found != nil {
		logger.InfoWithStatus(codes.Ok, "Drivers loader pod already exists")
		return nil // TODO: Update handling?
	}
	// Should we have an owner? Or should we just delete it once done? We cant have owner in different namespace
	// It would be convenient, if container would just exit.
	// Maybe, we should just replace this with completely different entry point and consolidate everything under single script
	// Agent does us no good. Container that runs on-time and just finished and removed afterwards would be simpler
	loaderContainer.Status.Status = "Active"
	if err := r.Status().Update(ctx, loaderContainer); err != nil {
		l.Error(err, "Failed to update status of container")
		return err

	}
	return nil
}

func (r *ContainerController) discoverDrive(ctx context.Context, executor util.Exec, drive string) string {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "discoverDrive", "drive", drive, "node_name", executor.GetNodeName())
	defer end()

	if strings.HasPrefix(drive, "aws_") {
		// aws discovery log, relying on PCI address as more persistent than device name, worth 1 hop
		slot := strings.TrimPrefix(drive, "aws_")
		slotInt, err := strconv.Atoi(slot)
		if err != nil {
			logger.WithValues("slot", slot).Error(err, "Error parsing slot")
			return drive
		}
		cmd := fmt.Sprintf("lspci -d 1d0f:cd01 | sort | awk '{print $1}' | head -n" + strconv.Itoa(slotInt+1) +
			" | tail -n1")
		stdout, stderr, err := executor.ExecNamed(ctx, "DiscoverDrivePciSlot", []string{"bash", "-ce", cmd})
		if err != nil {
			logger.WithValues("slot", slot, "stderr", stderr.String()).Error(err, "Error parsing PCI slot for drive")
			return ""
		}
		return fmt.Sprintf("/dev/disk/by-path/pci-0000:%s-nvme-1", strings.TrimSpace(stdout.String()))
	}
	return drive
}

func (r *ContainerController) initSignCloudDrives(ctx context.Context, executor util.Exec, drive string) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "initSignCloudDrives")
	defer end()

	logger.Info("Signing cloud drive", "drive", drive)
	cmd := fmt.Sprintf("weka local exec -- /weka/tools/weka_sign_drive %s", drive) // no-force and claims should keep us safe
	_, stderr, err := executor.ExecNamed(ctx, "WekaSignDrive", []string{"bash", "-ce", cmd})
	if err != nil {
		logger.Error(err, "Error pre-signing drive", "drive", drive, "stderr", stderr.String())
		return err
	}
	logger.InfoWithStatus(codes.Ok, "Cloud drives signed")
	return nil
}

func (r *ContainerController) checkIfLoaderFinished(ctx context.Context, pod *v1.Pod) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "checkIfLoaderFinished")
	defer end()

	logger.Info("Checking if loader finished")

	executor, err := util.NewExecInPod(pod)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return err
	}
	cmd := "cat /tmp/weka-drivers-loader"
	stdout, stderr, err := executor.ExecNamed(ctx, "CheckDriversLoaded", []string{"bash", "-ce", cmd})
	if err != nil {
		if strings.Contains(stderr.String(), "No such file or directory") {
			return errors.New("Loader not finished")
		}
		logger.Error(err, "Error checking if loader finished", "stderr", stderr.String)
		return err
	}
	if strings.TrimSpace(stdout.String()) == "drivers_loaded" {
		logger.InfoWithStatus(codes.Ok, "Loader finished")
		return nil
	}
	logger.InfoWithStatus(codes.Error, "Loader not finished")
	return errors.New(fmt.Sprintf("Loader not finished, unknown status %s", stdout.String()))
}

func (r *ContainerController) CleanupIfNeeded(ctx context.Context, container *wekav1alpha1.WekaContainer, pod *v1.Pod) error {
	kubeService := r.KubeService

	unschedulable := false
	unschedulableSince := time.Time{}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == v1.PodScheduled && condition.Status == v1.ConditionFalse && condition.Reason == "Unschedulable" {
			unschedulable = true
			unschedulableSince = condition.LastTransitionTime.Time
		}
	}

	if !unschedulable {
		return nil // cleanin up only unschedulable
	}

	if pod.Spec.NodeName != "" {
		return nil // cleaning only such that scheduled by node affinity
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "CleanupIfNeeded")
	defer end()

	_, err := kubeService.GetNode(ctx, pod.Spec.NodeName)
	if !apierrors.IsNotFound(err) {
		return nil // node still exists, handling only not found node
	}

	// We are safe to delete clients after a configurable while
	// TODO: Make configurable, for now we delete after 5 minutes since downtime
	// relying onlastTransitionTime of Unschedulable condition
	rescheduleAfter := 5 * time.Minute
	if container.IsBackend() {
		rescheduleAfter = 3 * time.Hour // TODO: Change, this is dev mode
	}
	if time.Since(unschedulableSince) > rescheduleAfter {
		logger.Info("Deleting unschedulable container")
		err := r.Delete(ctx, container)
		if err != nil {
			logger.Error(err, "Error deleting client container")
			return err
		}
		return errors.New("Pod is outdated and will be deleted")
	}
	return nil
}

func (r *ContainerController) finalizeContainer(ctx context.Context, container *wekav1alpha1.WekaContainer) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "finalizeContainer")
	defer end()

	// ensure no pod exists
	err := r.ensureNoPod(ctx, container)
	if r != nil {
		return err
	}

	err = r.CrdManager.EnsureTombstone(ctx, container)
	if err != nil {
		logger.Error(err, "Error ensuring tombstone")
		return err
	}
	return nil
}

func (r *ContainerController) ensureNoPod(ctx context.Context, container *wekav1alpha1.WekaContainer) error {
	// TODO: Can we search pods by ownership?

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureNoPod")
	defer end()

	pod := &v1.Pod{}
	err := r.Get(ctx, client.ObjectKey{Name: container.Name, Namespace: container.Namespace}, pod)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		logger.Error(err, "Error getting pod")
		return err
	}

	if pod.Status.Phase == v1.PodRunning {
		executor, err := util.NewExecInPod(pod)
		if err != nil {
			logger.Error(err, "Error creating executor")
			return err

		}
		// graceful shutdown (weka local stop -g) becoming a default, so should instruct explicitly when to do non graceful
		_, _, err = executor.ExecNamed(ctx, "AllowForceStop", []string{"bash", "-ce", "touch /tmp/.allow-force-stop"})
		if err != nil {
			return err
		}
	}

	err = r.Delete(ctx, pod)
	if err != nil {
		logger.Error(err, "Error deleting pod")
		return err
	}
	logger.AddEvent("Pod deleted")
	return errors.New("Pod deleted, reconciling for retry")
}
