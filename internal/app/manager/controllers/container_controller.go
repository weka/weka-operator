package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/weka/weka-operator/util"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"log"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"strings"
	"time"

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

const ()

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

//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;update;create

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

	result, err := r.reconcileManagementIP(ctx, container, actualPod)
	if err != nil {
		logger.Error(err, "Error reconciling management IP", "name", container.Name)
		return ctrl.Result{}, err
	}
	if result.Requeue {
		return result, nil
	}

	// pre-clusterize
	result, err = r.reconcileStatus(ctx, container, actualPod)
	if err != nil {
		logger.Error(err, "Error reconciling status", "name", container.Name)
		return ctrl.Result{}, err
	}
	if result.Requeue {
		return result, nil
	}

	// post-clusterize
	if !meta.IsStatusConditionTrue(container.Status.Conditions, CondJoinedCluster) {
		retry, err := r.reconcileClusterStatus(ctx, container, actualPod)
		if retry || err != nil {
			return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 3}, err
		}
		meta.SetStatusCondition(&container.Status.Conditions, metav1.Condition{Type: CondJoinedCluster,
			Status: metav1.ConditionTrue, Reason: "Success", Message: fmt.Sprintf("Joined cluster %s", container.Status.ClusterID)})
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
	if !meta.IsStatusConditionTrue(container.Status.Conditions, CondDrivesAdded) && container.Spec.NumDrives > 0 {
		retry, err := r.ensureDrives(ctx, container, actualPod)
		if retry || err != nil {
			return ctrl.Result{Requeue: true, RequeueAfter: 3 * time.Second}, err
		}
		meta.SetStatusCondition(&container.Status.Conditions, metav1.Condition{Type: CondDrivesAdded,
			Status: metav1.ConditionTrue, Reason: "Success", Message: fmt.Sprintf("Added %d drives", container.Spec.NumDrives)})
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
	logger := r.Logger.WithName("reconcileStatus")
	logger.Info("Reconciling status", "name", container.Name)

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
	bundledConfigMap := &v1.ConfigMap{}
	err := r.Get(ctx, client.ObjectKey{Namespace: util.GetPodNamespace(), Name: bootScriptConfigName}, bundledConfigMap)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Fatalln("Could not find operator-namespaced configmap for boot scripts")
		}
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
			driveSignTarget := getSignatureDevice(drive)

			cmd := fmt.Sprintf("hexdump -v -e '1/1 \"%%.2x\"' -s 8 -n 16 %s", driveSignTarget)
			stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
			if err != nil {
				if strings.Contains(stderr.String(), "No such file or directory") {
					r.Logger.Info("Drive does not exist or not pre-signed", "drive", driveSignTarget, "cmd", cmd, "containerName", container.Name, "stderr", stderr.String())
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
