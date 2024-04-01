package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/util"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/strings/slices"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"github.com/go-logr/logr"
	"github.com/kr/pretty"
	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const bootScriptConfigName = "weka-boot-scripts"

func NewContainerController(mgr ctrl.Manager) *ContainerController {
	return &ContainerController{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Logger: mgr.GetLogger().WithName("controllers").WithName("Container"),
	}
}

type ContainerController struct {
	client.Client
	Scheme *runtime.Scheme
	Logger logr.Logger
}

//+kubebuilder:rbac:groups=weka.weka.io,resources=wekaclusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekaclusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekaclusters/finalizers,verbs=update
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
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list

// Reconcile reconciles a WekaContainer resource
func (r *ContainerController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Logger.WithName("Reconcile")
	logger.Info("ContainerController.Reconcile() called")
	container, err := r.refreshContainer(ctx, req)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Container not found", "name", req.Name)
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Error refreshing container")
		return ctrl.Result{}, errors.Wrap(err, "ClientController.Reconcile")
	}

	r.initState(ctx, container)

	if container.GetDeletionTimestamp() != nil {
		logger.Info("Container is being deleted", "name", container.Name)
		return ctrl.Result{}, nil
	}

	desiredPod, err := resources.NewContainerFactory(container, logger).Create()
	if err != nil {
		logger.Error(err, "Error creating pod spec")
		return ctrl.Result{}, errors.Wrap(err, "ClientController.Reconcile")
	}
	if err := ctrl.SetControllerReference(container, desiredPod, r.Scheme); err != nil {
		logger.Error(err, "Error setting controller reference")
		return ctrl.Result{}, pretty.Errorf("Error setting controller reference", err, desiredPod)
	}

	err = r.ensureBootConfigMapInTargetNamespace(ctx, container)
	if err != nil {
		return ctrl.Result{}, pretty.Errorf("Error ensuring boot config map", err)
	}

	actualPod, err := r.refreshPod(ctx, container)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Creating pod", "name", container.Name)
			if err := r.Create(ctx, desiredPod); err != nil {
				return ctrl.Result{},
					pretty.Errorf("Error creating pod", err, desiredPod)
			}
			logger.Info("Pod created", "name", container.Name)
			return ctrl.Result{Requeue: true}, nil
		} else {
			logger.Info("Error refreshing pod", "name", container.Name)
			return ctrl.Result{}, errors.Wrap(err, "ClientController.Reconcile")
		}
	}

	if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondEnsureDrivers) &&
		!slices.Contains([]string{"drivers-loader", "dist"}, container.Spec.Mode) {
		err := r.reconcileDriversStatus(ctx, container, actualPod)
		if err != nil {
			if strings.Contains(err.Error(), "No such file or directory") {
				return ctrl.Result{Requeue: true}, nil
			}
			logger.Error(err, "Error reconciling drivers status", "name", container.Name)
			return ctrl.Result{}, err
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
	}

	result, err := r.reconcileManagementIP(ctx, container, actualPod)
	if err != nil {
		logger.Error(err, "Error reconciling management IP", "name", container.Name)
		return ctrl.Result{}, err
	}
	if result.Requeue {
		return result, nil
	}

	// pre-clusterize
	if !slices.Contains([]string{"drivers-loader"}, container.Spec.Mode) {
		result, err = r.reconcileStatus(ctx, container, actualPod)
		if err != nil {
			logger.Error(err, "Error reconciling status", "name", container.Name)
			return ctrl.Result{}, err
		}
		if result.Requeue {
			return result, nil
		}
	}

	if container.Spec.Mode == "drivers-loader" {
		err := r.checkIfLoaderFinished(ctx, actualPod)
		if err != nil {
			return ctrl.Result{RequeueAfter: time.Second * 3, Requeue: true}, err
		} else {
			// if drivers loaded we can delete this weka container
			err := r.Delete(ctx, container)
			if err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	if slices.Contains([]string{"drivers-loader", "dist"}, container.Spec.Mode) {
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
	}

	container, err = r.refreshContainer(ctx, req)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "refreshContainer")
	}
	if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondDrivesAdded) && container.Spec.NumDrives > 0 {
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
	}

	logger.Info("Reconcile completed", "name", container.Name)
	return ctrl.Result{}, nil
}

func (r *ContainerController) reconcileManagementIP(ctx context.Context, container *wekav1alpha1.WekaContainer, pod *v1.Pod) (ctrl.Result, error) {
	logger := r.Logger.WithName("reconcileManagementIP")
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
		getIpCmd = fmt.Sprintf("ip route show default | grep src | awk '/default/ {print $9}'")
	}

	stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", getIpCmd})
	if err != nil {
		logger.Error(err, "Error executing command", "stderr", stderr.String())
		return ctrl.Result{}, err
	}
	ipAddress := strings.TrimSpace(stdout.String())
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

func (r *ContainerController) reconcileStatus(ctx context.Context, container *wekav1alpha1.WekaContainer, pod *v1.Pod) (ctrl.Result, error) {
	if slices.Contains([]string{"drivers-loader"}, container.Spec.Mode) {
		return ctrl.Result{}, nil
	}
	logger := r.Logger.WithName(fmt.Sprintf("reconcileStatus-%s", container.Name))
	logger.Info("Reconciling status")

	executor, err := util.NewExecInPod(pod)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return ctrl.Result{}, err
	}

	statusCommand := fmt.Sprintf("weka local ps -J")
	stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", statusCommand})
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
	if len(response) != 1 {
		logger.Info("Expected exactly one container to be present, found ", len(response))
		return ctrl.Result{}, errors.New("expected exactly one container to be present")
	}

	status := response[0].RunStatus
	logger.Info("Status", "status", status)
	if container.Status.Status != status {
		logger.Info("Updating status", "from", container.Status.Status, "to", status)
		container.Status.Status = status
		if err := r.Status().Update(ctx, container); err != nil {
			logger.Error(err, "Error updating status")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{}, nil
}

func refreshContainer(r client.Reader, ctx context.Context, container *wekav1alpha1.WekaContainer) (*wekav1alpha1.WekaContainer, error) {
	ref := client.ObjectKey{Name: container.Name, Namespace: container.Namespace}
	container = &wekav1alpha1.WekaContainer{}
	if err := r.Get(ctx, ref, container); err != nil {
		return nil, errors.Wrap(err, "refreshContainer")
	}
	return container, nil
}

func (r *ContainerController) refreshContainer(ctx context.Context, req ctrl.Request) (*wekav1alpha1.WekaContainer, error) {
	container := &wekav1alpha1.WekaContainer{}
	if err := r.Get(ctx, req.NamespacedName, container); err != nil {
		return nil, errors.Wrap(err, "refreshContainer")
	}
	return container, nil
}

func (r *ContainerController) refreshPod(ctx context.Context, container *wekav1alpha1.WekaContainer) (*v1.Pod, error) {
	logger := r.Logger.WithName("refreshPod")
	pod := &v1.Pod{}
	key := client.ObjectKey{Name: container.Name, Namespace: container.Namespace}
	if err := r.Get(ctx, key, pod); err != nil {
		logger.Error(err, "Error refreshing pod", "key", key)
		return nil, err
	}

	return pod, nil
}

func (r *ContainerController) updatePod(ctx context.Context, pod *v1.Pod) error {
	logger := r.Logger.WithName("updatePod")
	if err := r.Update(ctx, pod); err != nil {
		logger.Error(err, "Error updating pod", "pod", pod)
		return err
	}
	return nil
}

func (r *ContainerController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.WekaContainer{}).
		Owns(&v1.Pod{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 10}).
		Complete(r)
}

func (r *ContainerController) ensureBootConfigMapInTargetNamespace(ctx context.Context, container *wekav1alpha1.WekaContainer) error {
	log := r.Logger.WithName("ensureBootConfigMapInTargetNamespace")
	bundledConfigMap := &v1.ConfigMap{}
	podNamespace, err := util.GetPodNamespace()
	if err != nil {
		log.Error(err, "Error getting pod namespace")
		return err
	}
	key := client.ObjectKey{Namespace: podNamespace, Name: bootScriptConfigName}
	if err := r.Get(ctx, key, bundledConfigMap); err != nil {
		if apierrors.IsNotFound(err) {
			log.Error(err, "Bundled config map not found")
			return err
		}
		log.Error(err, "Error getting bundled config map")
		return err
	}

	bootScripts := &v1.ConfigMap{}
	err = r.Get(ctx, client.ObjectKey{Namespace: container.Namespace, Name: bootScriptConfigName}, bootScripts)
	if err != nil {
		if apierrors.IsNotFound(err) {
			bootScripts.Namespace = container.Namespace
			bootScripts.Name = bootScriptConfigName
			bootScripts.Data = bundledConfigMap.Data
			if err := r.Create(ctx, bootScripts); err != nil {
				r.Logger.Error(err, "Error creating boot scripts config map")
			}
		}
	}

	if !util.IsEqualConfigMapData(bootScripts, bundledConfigMap) {
		bootScripts.Data = bundledConfigMap.Data
		if err := r.Update(ctx, bootScripts); err != nil {
			r.Logger.Error(err, "Error updating boot scripts config map")
		}
	}
	return nil
}

func (r *ContainerController) reconcileClusterStatus(ctx context.Context, container *wekav1alpha1.WekaContainer, pod *v1.Pod) (bool, error) {
	logger := r.Logger.WithName(fmt.Sprintf("reconcileClusterStatus-%s", container.Name))
	logger.Info("Reconciling cluster status")
	executor, err := util.NewExecInPod(pod)
	if err != nil {
		return true, nil
	}
	stdout, _, err := executor.Exec(ctx, []string{"bash", "-ce", "weka local status -J"})
	response := resources.WekaLocalStatusResponse{}
	err = json.Unmarshal(stdout.Bytes(), &response)
	if err != nil {
		return true, err
	}

	if _, ok := response[container.Spec.WekaContainerName]; !ok {
		return true, errors.New("container not found")
	}
	if len(response[container.Spec.WekaContainerName].Slots) == 0 {
		return true, errors.New("slots not found")
	}
	clusterId := response[container.Spec.WekaContainerName].Slots[0].ClusterID
	if clusterId == "" || clusterId == "00000000-0000-0000-0000-000000000000" {
		return true, nil
	}

	container.Status.ClusterID = clusterId
	if err := r.Status().Update(ctx, container); err != nil {
		return true, err
	}
	return false, nil
}

func (r *ContainerController) ensureDrives(ctx context.Context, container *wekav1alpha1.WekaContainer, pod *v1.Pod) (bool, error) {
	if container.Status.ClusterContainerID == nil {
		return true, nil
	}
	executor, err := util.NewExecInPod(pod)
	if err != nil {
		return true, err
	}

	numAdded := 0
	driveCursor := 0
DRIVES:
	for i := 0; i < container.Spec.NumDrives; i++ {
		for driveCursor < len(container.Spec.PotentialDrives) {
			r.Logger.Info("Adding drive", "drive", container.Spec.PotentialDrives[driveCursor],
				"position", i, "total", container.Spec.NumDrives)

			drive := container.Spec.PotentialDrives[driveCursor]
			drive = r.discoverDrive(ctx, executor, drive)
			driveSignTarget := getSignatureDevice(drive)

			cmd := fmt.Sprintf("hexdump -v -e '1/1 \"%%.2x\"' -s 8 -n 16 %s", driveSignTarget)
			stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
			if err != nil {
				if strings.Contains(stderr.String(), "No such file or directory") {
					if strings.HasPrefix(container.Spec.PotentialDrives[driveCursor], "aws_") {
						r.Logger.Info("Drive is not presigned, signing adhocy", "drive", driveSignTarget, "cmd", cmd, "containerName", container.Name, "stderr", stderr.String())
						if err := r.initSignAwsDrives(ctx, executor, drive); err != nil {
							r.Logger.Info("Error signing drive", "drive", drive, "containerName", container.Name, "stderr", stderr.String())
							// no return or continue on purpose, it is only opportunistic presigning while moving to next drive regardless
						}
					}
					r.Logger.Info("Drive does not exist or not pre-signed", "drive", drive, "signTarget", driveSignTarget, "cmd", cmd, "containerName", container.Name, "stderr", stderr.String())
					driveCursor++
					continue
				} else {
					return true, errors.Wrap(err, stderr.String()+"\n"+stdout.String())
				}
			}

			// Validate that disk is weka-signed
			presigned, err := r.isDrivePresigned(ctx, executor, drive)
			if !presigned {
				r.Logger.Info("Drive is not presigned, moving to next", "drive", drive)
				driveCursor++
				continue
			}

			if stdout.String() != "90f0090f90f0090f90f0090f90f0090f" {
				exists, err := r.isExistingCluster(ctx, stdout.String())
				if err != nil {
					return true, err
				}
				if exists {
					r.Logger.Info("Drive is already signed and cluster exists, moving to next", "drive", drive, "clusterId", stdout.String())
					driveCursor++
					continue
				} else {
					r.Logger.Info("Drive is already signed, but cluster does not exist, resigning", "drive", drive, "clusterId", stdout.String())
					err2 := r.claimDrive(ctx, container, executor, drive)
					if err2 != nil {
						r.Logger.Error(err2, "Error claiming drive for resigning", "drive", drive)
						driveCursor++
						continue
					}
					err2 = r.reSignDrive(ctx, executor, drive) // This changes UUID, effectively making claim obsolete
					if err2 != nil {
						r.Logger.Error(err2, "Error resigning drive", "drive", drive)
						driveCursor++
						continue
					}
				}
			}

			err = r.claimDrive(ctx, container, executor, drive)
			if err != nil {
				r.Logger.Error(err, "Error claiming drive", "drive", drive, "containerName", container.Name)
				driveCursor++
				continue
			}

			cmd = fmt.Sprintf("weka cluster drive add %d %s", *container.Status.ClusterContainerID, drive)
			_, stderr, err = executor.Exec(ctx, []string{"bash", "-ce", cmd})
			if err != nil {
				return true, errors.Wrap(err, stderr.String())
			} else {
				r.Logger.Info("Drive added into system", "drive", drive)
			}
			numAdded++
			driveCursor++
			continue DRIVES
		}
		return true, errors.New(fmt.Sprintf("Could not allocate drive %d", i))
	}
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

func (r *ContainerController) reSignDrive(ctx context.Context, executor *util.Exec, drive string) error {
	cmd := fmt.Sprintf("weka local exec -- /weka/tools/weka_sign_drive --force %s", drive)
	_, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
	if err != nil {
		r.Logger.Error(err, "Error signing drive", "drive", drive, "stderr", stderr.String())
	}
	return err
}

func (r *ContainerController) isExistingCluster(ctx context.Context, s string) (bool, error) {
	// TODO: Query by status?
	// TODO: Cache?

	clusterList := wekav1alpha1.WekaClusterList{}
	err := r.List(ctx, &clusterList)
	if err != nil {
		return false, err
	}
	for _, cluster := range clusterList.Items {
		// strip `-` from saved cluster name
		stripped := strings.ReplaceAll(cluster.Status.ClusterID, "-", "")
		if stripped == s {
			return true, nil
		}
	}
	return false, nil
}

func (r *ContainerController) validateNotMounted(ctx context.Context, executor *util.Exec, drive string) (bool, error) {
	return false, nil
}

func (r *ContainerController) isDrivePresigned(ctx context.Context, executor *util.Exec, drive string) (bool, error) {
	stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", "blkid -s PART_ENTRY_TYPE -o value -p " + getSignatureDevice(drive)})
	if err != nil {
		r.Logger.Error(err, "Error checking if drive is presigned", "drive", drive, "stderr", stderr.String(), "stdout", stdout.String())
		return false, errors.Wrap(err, stderr.String())
	}
	const WEKA_SIGNATURE = "993ec906-b4e2-11e7-a205-a0a8cd3ea1de"
	return strings.TrimSpace(stdout.String()) == WEKA_SIGNATURE, nil
}

func (r *ContainerController) claimDrive(ctx context.Context, container *wekav1alpha1.WekaContainer, executor *util.Exec, drive string) error {
	r.Logger.Info("Claiming drive", "drive", drive)
	driveUuid, err := r.getDriveUUID(ctx, executor, drive)
	if err != nil {
		r.Logger.Error(err, "Error getting drive UUID", "drive", drive)
		return err
	}
	r.Logger.Info("Claimed drive with number", "drive", drive, "UUID", driveUuid)

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
		r.Logger.Error(err, "Error setting owner reference", "drive", drive, "UUID", driveUuid)
		return err
	}
	r.Logger.Info("Set owner", "drive", drive, "UUID", driveUuid)

	err = r.Create(ctx, &claim)
	if err != nil {
		// get eixsting
		existingClaim := wekav1alpha1.DriveClaim{}
		err = r.Get(ctx, client.ObjectKey{Namespace: container.Namespace, Name: fmt.Sprintf("%s", driveUuid)}, &existingClaim)
		if err != nil {
			r.Logger.Error(err, "Error getting existing claim", "drive", drive, "UUID", driveUuid)
			return err
		}
		if existingClaim.OwnerReferences[0].UID != container.UID {
			r.Logger.Info("Drive already claimed by another container", "drive", drive, "UUID", driveUuid, "existingClaim", existingClaim)
			return errors.New("drive already claimed by another container")
		}
		return nil
	}
	return nil
}

func (r *ContainerController) getDriveUUID(ctx context.Context, executor *util.Exec, drive string) (string, error) {
	cmd := fmt.Sprintf("blkid -o value -s PARTUUID %s", getSignatureDevice(drive))
	stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
	if err != nil {
		return "", errors.Wrap(err, stderr.String())
	}
	serial := strings.TrimSpace(stdout.String())
	if serial == "" {
		r.Logger.Info("uuid not found for drive", "drive", drive, "usedCommand", cmd, "stdout", stdout.String(), "stderr", stderr.String())
		return "", errors.New("uuid not found")
	}
	return serial, nil
}

func (r *ContainerController) initState(ctx context.Context, container *wekav1alpha1.WekaContainer) {
	if container.Status.Conditions == nil {
		container.Status.Conditions = []metav1.Condition{}
	}

	if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondEnsureDrivers) && !slices.Contains([]string{"drivers-loader", "dist"}, container.Spec.Mode) {
		meta.SetStatusCondition(&container.Status.Conditions, metav1.Condition{Type: condition.CondEnsureDrivers, Status: metav1.ConditionFalse, Message: "Init"})
		_ = r.Status().Update(ctx, container)
	}
}

func (r *ContainerController) reconcileDriversStatus(ctx context.Context, container *wekav1alpha1.WekaContainer, pod *v1.Pod) error {
	if slices.Contains([]string{"drivers-loader", "dist"}, container.Spec.Mode) {
		return nil
	}

	executor, err := util.NewExecInPod(pod)
	if err != nil {
		return err
	}
	stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", "cat /tmp/weka-drivers.log"})
	if err != nil {
		return errors.Wrap(err, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		return nil
	}

	if container.Spec.DriversDistService != "" {
		err2 := r.ensureDriversLoader(ctx, container)
		if err2 != nil {
			r.Logger.Error(err2, "Error ensuring drivers loader", "container", container)
		}
	}

	return errors.New("Drivers not loaded")
}

func (r *ContainerController) ensureDriversLoader(ctx context.Context, container *wekav1alpha1.WekaContainer) error {
	pod, err := r.refreshPod(ctx, container)
	if err != nil {
		return err
	}
	loaderContainer := &wekav1alpha1.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "weka-drivers-loader-" + pod.Spec.NodeName,
			Namespace: "weka-operator-system",
		},
		Spec: wekav1alpha1.WekaContainerSpec{
			Image:              container.Spec.Image,
			Mode:               "drivers-loader",
			ImagePullSecret:    container.Spec.ImagePullSecret,
			Hugepages:          0,
			NodeAffinity:       container.Spec.NodeAffinity,
			DriversDistService: container.Spec.DriversDistService,
		},
	}

	found := &wekav1alpha1.WekaContainer{}
	err = r.Get(ctx, client.ObjectKey{Name: loaderContainer.Name, Namespace: loaderContainer.ObjectMeta.Namespace}, found)
	if err != nil {
		if apierrors.IsNotFound(err) {
			err = r.Create(ctx, loaderContainer)
			if err != nil {
				return err
			}
		}
	}
	if found != nil {
		return nil // TODO: Update handling?
	}
	// Should we have an owner? Or should we just delete it once done? We cant have owner in different namespace
	// It would be convenient, if container would just exit.
	// Maybe, we should just replace this with completely different entry point and consolidate everything under single script
	// Agent does us no good. Container that runs on-time and just finished and removed afterwards would be simpler
	return nil
}

func (r *ContainerController) discoverDrive(ctx context.Context, executor *util.Exec, drive string) string {
	if strings.HasPrefix(drive, "aws_") {
		// aws discovery log, relying on PCI address as more persistent than device name, worth 1 hop
		slot := strings.TrimPrefix(drive, "aws_")
		slotInt, err := strconv.Atoi(slot)
		if err != nil {
			r.Logger.Error(err, "Error parsing slot", "slot", slot)
			return drive
		}
		cmd := fmt.Sprintf("lspci -d 1d0f:cd01 | sort | awk '{print $1}' | head -n" + strconv.Itoa(slotInt+1) +
			" | tail -n1")
		stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
		if err != nil {
			r.Logger.Error(err, "Error executing command", "cmd", cmd, "stderr", stderr.String())
			return drive
		}
		return fmt.Sprintf("/dev/disk/by-path/pci-0000:%s-nvme-1", strings.TrimSpace(stdout.String()))
	}
	return drive
}

func (r *ContainerController) initSignAwsDrives(ctx context.Context, executor *util.Exec, drive string) error {
	cmd := fmt.Sprintf("weka local exec -- /weka/tools/weka_sign_drive %s", drive) // no-force and claims should keep us safe
	_, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
	if err != nil {
		r.Logger.Error(err, "Error presigning drive", "drive", drive, "stderr", stderr.String())
		return err
	}
	return nil
}

func (r *ContainerController) checkIfLoaderFinished(ctx context.Context, pod *v1.Pod) error {
	executor, err := util.NewExecInPod(pod)
	if err != nil {
		return err
	}
	cmd := "cat /tmp/weka-drivers-loader"
	stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
	if err != nil {
		r.Logger.Error(err, "Error checking if loader finished", "stderr", stderr.String)
		return err
	}
	if strings.TrimSpace(stdout.String()) == "drivers_loaded" {
		return nil
	}
	return errors.New(fmt.Sprintf("Loader not finished, unknown status %s", stdout.String()))
}
