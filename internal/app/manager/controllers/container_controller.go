package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-logr/logr"
	"github.com/kr/pretty"
	"github.com/pkg/errors"
	"strconv"
	"strings"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/util"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/strings/slices"
	"log"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
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
	ctx, span := instrumentation.Tracer.Start(ctx, "WekaContainerReconcile")
	defer span.End()
	logger := r.Logger.
		WithName("Reconcile").
		WithValues("trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String()).
		WithValues("container", req.Name, "namespace", req.Namespace).
		WithValues("last_pod_status", func() string {
			container := &wekav1alpha1.WekaContainer{}
			if err := r.Get(ctx, req.NamespacedName, container); err != nil {
				if apierrors.IsNotFound(err) {
					return "NOT_FOUND"
				}
				return "FETCH_ERROR"
			}
			pod := &v1.Pod{}
			key := client.ObjectKey{Name: container.Name, Namespace: container.Namespace}
			if err := r.Get(ctx, key, pod); err != nil {
				if apierrors.IsNotFound(err) {
					return "NOT_FOUND"
				}
				return "FETCH_ERROR"
			}
			return string(pod.Status.Phase)
		}())
	logger.Info("Reconcile() called")
	defer logger.Info("Reconcile() finished")
	container, err := r.refreshContainer(ctx, req)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Container not found")
			span.SetStatus(codes.Unset, "Container not found")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Error refreshing container")
		span.SetStatus(codes.Error, "Error refreshing container")
		span.RecordError(err)
		return ctrl.Result{}, errors.Wrap(err, "ClientController.Reconcile")
	}

	span.AddEvent("Container refreshed", trace.WithAttributes(attribute.String("container", container.Name)))
	r.initState(ctx, container)

	if container.GetDeletionTimestamp() != nil {
		logger.Info("Container is being deleted", "name", container.Name)
		span.SetStatus(codes.Unset, "Container is being deleted")
		return ctrl.Result{}, nil
	}

	desiredPod, err := resources.NewContainerFactory(container, logger).Create()
	if err != nil {
		logger.Error(err, "Error creating pod spec")
		span.RecordError(err)
		span.SetStatus(codes.Error, "Error creating pod spec")
		return ctrl.Result{}, errors.Wrap(err, "ClientController.Reconcile")
	}

	if err := ctrl.SetControllerReference(container, desiredPod, r.Scheme); err != nil {
		logger.Error(err, "Error setting controller reference")
		span.RecordError(err)
		span.SetStatus(codes.Error, "Error setting controller reference")
		return ctrl.Result{}, pretty.Errorf("Error setting controller reference", err, desiredPod)
	}

	err = r.ensureBootConfigMapInTargetNamespace(ctx, container)
	if err != nil {
		return ctrl.Result{}, pretty.Errorf("Error ensuring boot config map", err)
	}

	actualPod, err := r.refreshPod(ctx, container)
	if err != nil {
		if apierrors.IsNotFound(err) {
			span.AddEvent("Pod not found, creating", trace.WithAttributes(attribute.String("pod", container.Name)))
			logger.Info("Creating pod", "name", container.Name)
			if err := r.Create(ctx, desiredPod); err != nil {
				span.SetStatus(codes.Error, "Error creating pod")
				span.RecordError(err)
				return ctrl.Result{},
					pretty.Errorf("Error creating pod", err, desiredPod)
			}
			logger.Info("Pod created", "name", container.Name)
			span.AddEvent("Pod created", trace.WithAttributes(attribute.String("pod", container.Name)))
			return ctrl.Result{Requeue: true}, nil
		} else {
			logger.Info("Error refreshing pod", "name", container.Name)
			span.SetStatus(codes.Error, "Error refreshing pod")
			return ctrl.Result{}, errors.Wrap(err, "ClientController.Reconcile")
		}
	}

	if !meta.IsStatusConditionTrue(container.Status.Conditions, condition.CondEnsureDrivers) &&
		!slices.Contains([]string{"drivers-loader", "dist"}, container.Spec.Mode) {
		err := r.reconcileDriversStatus(ctx, container, actualPod)
		if err != nil {
			if strings.Contains(err.Error(), "No such file or directory") {
				span.AddEvent("Drivers log not found", trace.WithAttributes(attribute.String("container", container.Name)))
				return ctrl.Result{Requeue: true}, nil
			}
			span.RecordError(err)
			span.SetStatus(codes.Error, "Error reconciling drivers status")
			logger.Error(err, "Error reconciling drivers status", "name", container.Name)
			return ctrl.Result{}, nil
		}
		meta.SetStatusCondition(&container.Status.Conditions, metav1.Condition{
			Type:   condition.CondEnsureDrivers,
			Status: metav1.ConditionTrue, Reason: "Success", Message: "Drivers are ensured",
		})
		err = r.Status().Update(ctx, container)
		if err != nil {
			logger.Error(err, "Error updating status for drivers ensured")
			span.RecordError(err)
			span.SetStatus(codes.Error, "Error updating status for drivers ensured")
			return ctrl.Result{}, err
		}
	}

	result, err := r.reconcileManagementIP(ctx, container, actualPod)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Error reconciling management IP")
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
	}

	logger.Info("Reconcile completed", "name", container.Name)
	return ctrl.Result{}, nil
}

func (r *ContainerController) reconcileManagementIP(ctx context.Context, container *wekav1alpha1.WekaContainer, pod *v1.Pod) (ctrl.Result, error) {
	ctx, span := instrumentation.Tracer.Start(ctx, "reconcileManagementIP")
	defer span.End()
	logger := r.Logger.WithName("reconcileManagementIP").WithValues("trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String())

	//// TODO: maybe another solution is needed, to consult @anton
	//if container.Spec.Mode == "dist" || container.Spec.Mode == "drivers-loader" || container.Spec.Mode == "drivers-builder" {
	//	logger.Info(fmt.Sprintf("Skipping management IP reconciliation for %s container", container.Spec.Mode))
	//	return ctrl.Result{}, nil
	//}

	if container.Status.ManagementIP != "" {
		return ctrl.Result{}, nil
	}
	executor, err := util.NewExecInPod(pod)
	if err != nil {
		span.AddEvent("Error creating executor", trace.WithAttributes(attribute.String("container", container.Name)))
		span.RecordError(err)
		span.SetStatus(codes.Error, "Error creating executor")
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
	span.AddEvent("Got management IP", trace.WithAttributes(attribute.String("ip", ipAddress)))
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
	ctx, span := instrumentation.Tracer.Start(ctx, "reconcileStatus")
	defer span.End()
	logger := r.Logger.WithName("reconcileStatus").
		WithValues("trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String())

	if slices.Contains([]string{"drivers-loader"}, container.Spec.Mode) {
		return ctrl.Result{}, nil
	}
	logger.Info("Reconciling status")
	span.AddEvent("Reconciling status", trace.WithAttributes(attribute.String("container", container.Name)))

	executor, err := util.NewExecInPod(pod)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return ctrl.Result{}, err
	}

	statusCommand := fmt.Sprintf("weka local ps -J")
	stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", statusCommand})
	if err != nil {
		span.SetStatus(codes.Error, "Error executing command")
		span.RecordError(err)
		logger.Error(err, "Error executing command", "command", statusCommand, "stderr", stderr.String())
		return ctrl.Result{}, err
	}
	response := []resources.WekaLocalPs{}
	err = json.Unmarshal(stdout.Bytes(), &response)
	if err != nil {
		span.AddEvent("Error unmarshalling response", trace.WithAttributes(attribute.String("stdout", stdout.String())))
		span.RecordError(err)
		logger.Error(err, "Error unmarshalling response", "stdout", stdout.String())
		return ctrl.Result{}, err
	}
	if len(response) != 1 {
		logger.Info(fmt.Sprintf("Expected exactly one container to be present, found %d", len(response)))
		span.SetStatus(codes.Error, "Expected exactly one container to be present")
		return ctrl.Result{}, nil
	}

	status := response[0].RunStatus
	logger.Info("Status", "status", status)
	if container.Status.Status != status {
		logger.Info("Updating status", "from", container.Status.Status, "to", status)
		container.Status.Status = status
		if err := r.Status().Update(ctx, container); err != nil {
			logger.Error(err, "Error updating status")
			span.AddEvent("Error updating status", trace.WithAttributes(attribute.String("status", status)))
			span.RecordError(err)
			return ctrl.Result{}, err
		}
		span.AddEvent("Status updated", trace.WithAttributes(attribute.String("status", status)))
		return ctrl.Result{Requeue: true}, nil
	}
	span.SetStatus(codes.Ok, "Status reconciled")
	return ctrl.Result{}, nil
}

func (r *ContainerController) refreshContainer(ctx context.Context, req ctrl.Request) (*wekav1alpha1.WekaContainer, error) {
	ctx, span := instrumentation.Tracer.Start(ctx, "refreshContainer")
	defer span.End()

	container := &wekav1alpha1.WekaContainer{}
	if err := r.Get(ctx, req.NamespacedName, container); err != nil {
		span.AddEvent("Error refreshing container", trace.WithAttributes(attribute.String("container", req.Name)))
		span.RecordError(err)
		return nil, errors.Wrap(err, "refreshContainer")
	}
	span.AddEvent("Container refreshed", trace.WithAttributes(attribute.String("container", req.Name)))
	return container, nil
}

func (r *ContainerController) refreshPod(ctx context.Context, container *wekav1alpha1.WekaContainer) (*v1.Pod, error) {
	ctx, span := instrumentation.Tracer.Start(ctx, "refreshPod")
	defer span.End()
	logger := r.Logger.WithName("refreshPod").WithValues("trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String())
	pod := &v1.Pod{}
	key := client.ObjectKey{Name: container.Name, Namespace: container.Namespace}
	if err := r.Get(ctx, key, pod); err != nil {
		logger.Error(err, "Error refreshing pod", "key", key)
		span.SetStatus(codes.Error, "Error refreshing pod")
		span.RecordError(err)
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
	ctx, span := instrumentation.Tracer.Start(ctx, "ensureBootConfigMapInTargetNamespace")
	defer span.End()
	logger := r.Logger.WithName("ensureBootConfigMapInTargetNamespace").WithValues("trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String())

	bundledConfigMap := &v1.ConfigMap{}
	podNamespace, err := util.GetPodNamespace()
	if err != nil {
		logger.Error(err, "Error getting pod namespace")
		return err
	}
	key := client.ObjectKey{Namespace: podNamespace, Name: bootScriptConfigName}
	if err := r.Get(ctx, key, bundledConfigMap); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Error(err, "Bundled config map not found")
			return err
		}
		logger.Error(err, "Error getting bundled config map")
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
				span.RecordError(err)
				span.SetStatus(codes.Error, "Error creating boot scripts config map in designated namespace")
				logger.Error(err, "Error creating boot scripts config map")
			}
			span.SetStatus(codes.Ok, "Created boot scripts config map in designated namespace")
		}
	}

	if !util.IsEqualConfigMapData(bootScripts, bundledConfigMap) {
		bootScripts.Data = bundledConfigMap.Data
		if err := r.Update(ctx, bootScripts); err != nil {
			span.SetStatus(codes.Ok, "Updated and reconciled boot scripts config map in designated namespace")
			logger.Error(err, "Error updating boot scripts config map")
			return err
		}
	}
	return nil
}

func (r *ContainerController) reconcileClusterStatus(ctx context.Context, container *wekav1alpha1.WekaContainer, pod *v1.Pod) (bool, error) {
	ctx, span := instrumentation.Tracer.Start(ctx, "ContainerReconcileClusterStatus")
	defer span.End()
	logger := r.Logger.WithName(fmt.Sprintf("reconcileClusterStatus-%s", container.Name)).WithValues("trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String())
	logger.Info("Reconciling cluster status")
	span.AddEvent("Reconciling cluster status", trace.WithAttributes(attribute.String("container", container.Name)))
	executor, err := util.NewExecInPod(pod)
	if err != nil {
		return true, nil
	}
	span.AddEvent("Querying weka local status", trace.WithAttributes(attribute.String("container", container.Name)))
	stdout, _, err := executor.Exec(ctx, []string{"bash", "-ce", "weka local status -J"})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Error querying weka local status")
		return true, err
	}
	span.AddEvent("Parsing weka local status", trace.WithAttributes(attribute.String("container", container.Name)))
	response := resources.WekaLocalStatusResponse{}
	err = json.Unmarshal(stdout.Bytes(), &response)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Error parsing weka local status")
		return true, err
	}

	if _, ok := response[container.Spec.WekaContainerName]; !ok {
		span.SetStatus(codes.Error, "Container not found")
		return true, errors.New("container not found")
	}
	if len(response[container.Spec.WekaContainerName].Slots) == 0 {
		span.SetStatus(codes.Error, "Slots not found")
		return true, errors.New("slots not found")
	}
	clusterId := response[container.Spec.WekaContainerName].Slots[0].ClusterID
	if clusterId == "" || clusterId == "00000000-0000-0000-0000-000000000000" {
		span.SetStatus(codes.Unset, "Cluster not ready")
		return true, nil
	}

	container.Status.ClusterID = clusterId
	span.SetStatus(codes.Ok, "Cluster created and its GUID updated in WekaContainer status")
	if err := r.Status().Update(ctx, container); err != nil {
		return true, err
	}
	return false, nil
}

func (r *ContainerController) ensureDrives(ctx context.Context, container *wekav1alpha1.WekaContainer, pod *v1.Pod) (bool, error) {
	ctx, span := instrumentation.Tracer.Start(ctx, "ContainerEnsureDrives")
	defer span.End()
	logger := r.Logger.WithName(fmt.Sprintf("ensureDrives-%s", container.Name)).WithValues("trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String())
	if container.Status.ClusterContainerID == nil {
		span.SetStatus(codes.Error, "ClusterContainerID not set, cannot ensure drives")
		return true, nil
	}
	executor, err := util.NewExecInPod(pod)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Error creating executor")
		return true, err
	}

	numAdded := 0
	driveCursor := 0
DRIVES:
	for i := 0; i < container.Spec.NumDrives; i++ {
		for driveCursor < len(container.Spec.PotentialDrives) {
			span.AddEvent("Adding drive", trace.WithAttributes(attribute.String("drive", container.Spec.PotentialDrives[driveCursor]),
				attribute.Int("position", i), attribute.Int("total_drives", container.Spec.NumDrives)))
			logger.Info("Adding drive", "drive", container.Spec.PotentialDrives[driveCursor],
				"position", i, "total", container.Spec.NumDrives)

			drive := container.Spec.PotentialDrives[driveCursor]
			drive = r.discoverDrive(ctx, executor, drive)
			driveSignTarget := getSignatureDevice(drive)

			span.AddEvent("Verifying drive signature", trace.WithAttributes(attribute.String("drive", drive)))
			cmd := fmt.Sprintf("hexdump -v -e '1/1 \"%%.2x\"' -s 8 -n 16 %s", driveSignTarget)
			stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
			if err != nil {
				span.RecordError(err)
				if strings.Contains(stderr.String(), "No such file or directory") {
					if strings.HasPrefix(container.Spec.PotentialDrives[driveCursor], "aws_") {
						span.AddEvent("Drive is not presigned, signing adhocy", trace.WithAttributes(attribute.String("drive", driveSignTarget)))
						logger.Info("Drive is not presigned, signing adhocy", "drive", driveSignTarget, "cmd", cmd, "containerName", container.Name, "stderr", stderr.String())
						if err := r.initSignAwsDrives(ctx, executor, drive); err != nil {
							r.Logger.Info("Error signing drive", "drive", drive, "containerName", container.Name, "stderr", stderr.String())
							// no return or continue on purpose, it is only opportunistic presigning while moving to next drive regardless
						}
					}
					logger.Info("Drive does not exist or not pre-signed", "drive", drive, "signTarget", driveSignTarget, "cmd", cmd, "containerName", container.Name, "stderr", stderr.String())
					span.AddEvent("Drive does not exist or not pre-signed", trace.WithAttributes(attribute.String("drive", drive), attribute.String("signTarget", driveSignTarget), attribute.String("stderr", stderr.String())))
					driveCursor++
					continue
				} else {
					return true, errors.Wrap(err, stderr.String()+"\n"+stdout.String())
				}
			}

			// Validate that disk is weka-signed
			span.AddEvent("Checking if drive is presigned", trace.WithAttributes(attribute.String("drive", drive)))
			presigned, err := r.isDrivePresigned(ctx, executor, drive)
			if !presigned {
				span.AddEvent("Drive is not presigned", trace.WithAttributes(attribute.String("drive", drive)))
				logger.Info("Drive is not presigned, moving to next", "drive", drive)
				driveCursor++
				continue
			}

			if stdout.String() != "90f0090f90f0090f90f0090f90f0090f" {
				span.AddEvent("Drive has Weka signature on it, verifying ownership", trace.WithAttributes(attribute.String("drive", drive)))
				exists, err := r.isExistingCluster(ctx, stdout.String())
				if err != nil {
					return true, err
				}
				if exists {
					logger.Info("Drive is already signed and cluster exists, moving to next", "drive", drive, "clusterId", stdout.String())
					span.AddEvent("Drive is already signed and cluster exists, moving to next", trace.WithAttributes(attribute.String("drive", drive), attribute.String("clusterId", stdout.String())))
					driveCursor++
					continue
				} else {
					logger.Info("Drive is already signed, but cluster does not exist, resigning", "drive", drive, "clusterId", stdout.String())
					span.AddEvent("Drive is already signed, but cluster does not exist, resigning", trace.WithAttributes(attribute.String("drive", drive), attribute.String("clusterId", stdout.String())))
					err2 := r.claimDrive(ctx, container, executor, drive)
					if err2 != nil {
						logger.Error(err2, "Error claiming drive for resigning", "drive", drive)
						span.RecordError(err)
						span.AddEvent("Error claiming drive for resigning", trace.WithAttributes(attribute.String("drive", drive)))
						driveCursor++
						continue
					}
					err2 = r.reSignDrive(ctx, executor, drive) // This changes UUID, effectively making claim obsolete
					if err2 != nil {
						span.RecordError(err2)
						span.AddEvent("Error resigning drive", trace.WithAttributes(attribute.String("drive", drive)))
						logger.Error(err2, "Error resigning drive", "drive", drive)
						driveCursor++
						continue
					}
				}
			}

			span.AddEvent("Claiming drive", trace.WithAttributes(attribute.String("drive", drive)))

			err = r.claimDrive(ctx, container, executor, drive)
			if err != nil {
				logger.Error(err, "Error claiming drive", "drive", drive, "containerName", container.Name)
				span.AddEvent("Error claiming drive", trace.WithAttributes(attribute.String("drive", drive)))
				driveCursor++
				continue
			}

			span.AddEvent("Adding drive into system", trace.WithAttributes(attribute.String("drive", drive)))
			cmd = fmt.Sprintf("weka cluster drive add %d %s", *container.Status.ClusterContainerID, drive)
			_, stderr, err = executor.Exec(ctx, []string{"bash", "-ce", cmd})
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, "Error adding drive into system")
				return true, errors.Wrap(err, stderr.String())
			} else {
				span.SetStatus(codes.Ok, "Drive added into system")
				logger.Info("Drive added into system", "drive", drive)
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
	ctx, span := instrumentation.Tracer.Start(ctx, "ContainerIsExistingCluster")
	defer span.End()
	span.AddEvent("Verifying for existing cluster", trace.WithAttributes(attribute.String("clusterId", s)))
	clusterList := wekav1alpha1.WekaClusterList{}
	err := r.List(ctx, &clusterList)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Error listing clusters")
		return false, err
	}
	for _, cluster := range clusterList.Items {
		// strip `-` from saved cluster name
		stripped := strings.ReplaceAll(cluster.Status.ClusterID, "-", "")
		if stripped == s {
			span.SetStatus(codes.Ok, "Cluster found")
			return true, nil
		}
	}
	span.SetStatus(codes.Ok, "Cluster not found")
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
	ctx, span := instrumentation.Tracer.Start(ctx, "ContainerClaimDrive")
	defer span.End()
	logger := r.Logger.WithName("claimDrive").WithValues("drive", drive, "container", container.Name, "trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String())
	logger.Info("Claiming drive", "drive", drive)
	span.AddEvent("Claiming drive", trace.WithAttributes(attribute.String("drive", drive)))
	driveUuid, err := r.getDriveUUID(ctx, executor, drive)
	if err != nil {
		logger.Error(err, "Error getting drive UUID", "drive", drive)
		span.SetStatus(codes.Error, "Error getting drive UUID")
		span.RecordError(err)
		return err
	}
	logger.Info("Claimed drive with number", "drive", drive, "UUID", driveUuid)
	span.AddEvent("Claimed drive with number", trace.WithAttributes(attribute.String("drive", drive), attribute.String("UUID", driveUuid)))

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
		logger.Error(err, "Error setting owner reference", "drive", drive, "UUID", driveUuid)
		return err
	}
	logger.Info("Set owner", "drive", drive, "UUID", driveUuid)

	span.AddEvent("Creating drive claim", trace.WithAttributes(attribute.String("drive", drive), attribute.String("UUID", driveUuid)))
	err = r.Create(ctx, &claim)
	if err != nil {
		// get eixsting
		existingClaim := wekav1alpha1.DriveClaim{}
		err = r.Get(ctx, client.ObjectKey{Namespace: container.Namespace, Name: fmt.Sprintf("%s", driveUuid)}, &existingClaim)
		if err != nil {
			logger.Error(err, "Error getting existing claim", "drive", drive, "UUID", driveUuid)
			span.RecordError(err)
			span.SetStatus(codes.Error, "Error getting existing claim")
			return err
		}
		if existingClaim.OwnerReferences[0].UID != container.UID {
			logger.Info("Drive already claimed by another container", "drive", drive, "UUID", driveUuid, "existingClaim", existingClaim)
			span.RecordError(err)
			span.SetStatus(codes.Error, "Drive already claimed by another container")
			return errors.New("drive already claimed by another container")
		}
		return nil
	}
	return nil
}

func (r *ContainerController) getDriveUUID(ctx context.Context, executor *util.Exec, drive string) (string, error) {
	ctx, span := instrumentation.Tracer.Start(ctx, "ContainerGetDriveUUID")
	defer span.End()
	logger := r.Logger.WithName("getDriveUUID").WithValues("drive", drive, "trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String())
	cmd := fmt.Sprintf("blkid -o value -s PARTUUID %s", getSignatureDevice(drive))
	stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
	if err != nil {
		logger.Error(err, "Error getting drive UUID", "drive", drive, "stderr", stderr.String())
		span.RecordError(err)
		span.SetStatus(codes.Error, "Error getting drive UUID")
		return "", errors.Wrap(err, stderr.String())
	}
	serial := strings.TrimSpace(stdout.String())
	if serial == "" {
		span.SetStatus(codes.Error, "UUID not found for drive")
		span.RecordError(err)
		logger.Info("uuid not found for drive", "drive", drive, "usedCommand", cmd, "stdout", stdout.String(), "stderr", stderr.String())
		return "", errors.New("uuid not found")
	}
	span.SetStatus(codes.Ok, "UUID found for drive")
	return serial, nil
}

func (r *ContainerController) initState(ctx context.Context, container *wekav1alpha1.WekaContainer) {
	if container.Status.Conditions == nil {
		container.Status.Conditions = []metav1.Condition{}
	}

	// All container types are being set to False on init
	// This includes types not listed here (beyond dist and drivers-loader)
	// TODO: Is this expected?
	if !container.DriversReady() && container.SupportsEnsureDriversCondition() {
		meta.SetStatusCondition(&container.Status.Conditions, metav1.Condition{Type: condition.CondEnsureDrivers, Status: metav1.ConditionFalse, Message: "Init"})
		_ = r.Status().Update(ctx, container)
	}
}

func (r *ContainerController) reconcileDriversStatus(ctx context.Context, container *wekav1alpha1.WekaContainer, pod *v1.Pod) error {
	ctx, span := instrumentation.Tracer.Start(ctx, "ContainerReconcileDriversStatus")
	defer span.End()
	logger := r.Logger.WithName("reconcileDriversStatus").WithValues("trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String())
	if slices.Contains([]string{"drivers-loader", "dist", "drivers-builder"}, container.Spec.Mode) {
		return nil
	}

	executor, err := util.NewExecInPod(pod)
	if err != nil {
		logger.Error(err, "Error creating executor")
		span.RecordError(err)
		span.SetStatus(codes.Error, "Error creating executor")
		return err
	}
	stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", "cat /tmp/weka-drivers.log"})
	if err != nil {
		logger.Error(err, "Error executing command", "stderr", stderr.String())
		span.RecordError(err)
		span.SetStatus(codes.Error, "Error executing command")
		return errors.Wrap(err, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		span.AddEvent("Drivers already loaded", trace.WithAttributes(attribute.String("container", container.Name)))
		span.SetStatus(codes.Ok, "Drivers already loaded")
		return nil
	}

	if container.Spec.DriversDistService != "" {
		span.AddEvent("Ensuring drivers loader", trace.WithAttributes(attribute.String("container", container.Name)))
		err2 := r.ensureDriversLoader(ctx, container)
		if err2 != nil {
			r.Logger.Error(err2, "Error ensuring drivers loader", "container", container)
		}
	}

	return errors.New("Drivers not loaded")
}

func (r *ContainerController) ensureDriversLoader(ctx context.Context, container *wekav1alpha1.WekaContainer) error {
	ctx, span := instrumentation.Tracer.Start(ctx, "ContainerEnsureDriversLoader")
	defer span.End()
	logger := r.Logger.WithName("ensureDriversLoader").WithValues("trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String())
	logger.Info("Ensuring drivers loader")
	span.AddEvent("Ensuring drivers loader pod existence", trace.WithAttributes(attribute.String("container", container.Name)))
	pod, err := r.refreshPod(ctx, container)
	if err != nil {
		span.SetStatus(codes.Error, "Error refreshing pod")
		span.RecordError(err)
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
			span.AddEvent("Creating drivers loader pod", trace.WithAttributes(attribute.String("container", container.Name)))
			err = r.Create(ctx, loaderContainer)
			if err != nil {
				span.AddEvent("Error creating drivers loader pod", trace.WithAttributes(attribute.String("container", container.Name)))
				span.RecordError(err)
				return err
			}
		}
	}
	if found != nil {
		span.SetStatus(codes.Ok, "Drivers loader pod already exists")
		return nil // TODO: Update handling?
	}
	// Should we have an owner? Or should we just delete it once done? We cant have owner in different namespace
	// It would be convenient, if container would just exit.
	// Maybe, we should just replace this with completely different entry point and consolidate everything under single script
	// Agent does us no good. Container that runs on-time and just finished and removed afterwards would be simpler
	loaderContainer.Status.Status = "Active"
	if err := r.Status().Update(ctx, loaderContainer); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Error updating status")
		return err

	}
	return nil
}

func (r *ContainerController) discoverDrive(ctx context.Context, executor *util.Exec, drive string) string {
	ctx, span := instrumentation.Tracer.Start(ctx, "ContainerDiscoverDrive")
	defer span.End()
	logger := r.Logger.WithName("discoverDrive").
		WithValues("trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String()).
		WithValues("drive", drive, "node_name", executor.Pod.Spec.NodeName)
	if strings.HasPrefix(drive, "aws_") {
		// aws discovery log, relying on PCI address as more persistent than device name, worth 1 hop
		slot := strings.TrimPrefix(drive, "aws_")
		slotInt, err := strconv.Atoi(slot)
		if err != nil {
			span.SetStatus(codes.Error, "Error parsing slot for drive")
			span.RecordError(err)
			logger.Error(err, "Error parsing slot", "slot", slot)
			return drive
		}
		cmd := fmt.Sprintf("lspci -d 1d0f:cd01 | sort | awk '{print $1}' | head -n" + strconv.Itoa(slotInt+1) +
			" | tail -n1")
		stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
		if err != nil {
			span.SetStatus(codes.Error, "Error parsing PCI slot for drive")
			span.RecordError(err)
			logger.Error(err, "Error executing command", "cmd", cmd, "stderr", stderr.String())
			return drive
		}
		return fmt.Sprintf("/dev/disk/by-path/pci-0000:%s-nvme-1", strings.TrimSpace(stdout.String()))
	}
	return drive
}

func (r *ContainerController) initSignAwsDrives(ctx context.Context, executor *util.Exec, drive string) error {
	ctx, span := instrumentation.Tracer.Start(ctx, "ContainerInitSignAwsDrives")
	defer span.End()
	logger := r.Logger.WithName("initSignAwsDrives").WithValues("trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String())
	logger.Info("Signing AWS drives", "drive", drive)
	cmd := fmt.Sprintf("weka local exec -- /weka/tools/weka_sign_drive %s", drive) // no-force and claims should keep us safe
	_, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Error signing AWS drives")
		logger.Error(err, "Error presigning drive", "drive", drive, "stderr", stderr.String())
		return err
	}
	span.SetStatus(codes.Ok, "AWS drives signed")
	return nil
}

func (r *ContainerController) checkIfLoaderFinished(ctx context.Context, pod *v1.Pod) error {
	ctx, span := instrumentation.Tracer.Start(ctx, "ContainerCheckIfLoaderFinished")
	defer span.End()
	logger := r.Logger.WithName("checkIfLoaderFinished").
		WithValues("trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String()).
		WithValues("pod", pod.Name, "namespace", pod.Namespace)

	logger.Info("Checking if loader finished")
	span.AddEvent("Checking if loader finished", trace.WithAttributes(attribute.String("pod", pod.Name)))

	executor, err := util.NewExecInPod(pod)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Error creating executor")
		return err
	}
	cmd := "cat /tmp/weka-drivers-loader"
	stdout, stderr, err := executor.Exec(ctx, []string{"bash", "-ce", cmd})
	if err != nil {
		if strings.Contains(stderr.String(), "No such file or directory") {
			return errors.New("Loader not finished")
		}
		logger.Error(err, "Error checking if loader finished", "stderr", stderr.String)
		span.AddEvent("Error checking if loader finished", trace.WithAttributes(attribute.String("stderr", stderr.String())))
		span.RecordError(err)
		return err
	}
	if strings.TrimSpace(stdout.String()) == "drivers_loaded" {
		span.SetStatus(codes.Ok, "Loader finished")
		return nil
	}
	span.SetStatus(codes.Unset, "Loader not finished")
	return errors.New(fmt.Sprintf("Loader not finished, unknown status %s", stdout.String()))
}
