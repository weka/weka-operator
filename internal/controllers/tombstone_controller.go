package controllers

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
	"k8s.io/apimachinery/pkg/types"

	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type TombstoneReconciller struct {
	Client      client.Client
	Scheme      *runtime.Scheme
	Manager     ctrl.Manager
	Recorder    record.EventRecorder
	ExecService exec.ExecService
	KubeService kubernetes.KubeService
	config      TombstoneConfig
}

func (r *TombstoneReconciller) SetupWithManager(mgr ctrl.Manager, reconciler reconcile.Reconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.Tombstone{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: config.Config.MaxWorkers.Tombstone}).
		Complete(reconciler)
}

type TombstoneConfig struct {
	EnableTombstoneGc   bool
	TombstoneGcInterval time.Duration
	TombstoneExpiration time.Duration
	DeleteOnNodeMissing bool
}

func NewTombstoneController(mgr ctrl.Manager, config TombstoneConfig) *TombstoneReconciller {
	restConfig := mgr.GetConfig()
	reconciler := &TombstoneReconciller{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		Manager:     mgr,
		Recorder:    mgr.GetEventRecorderFor("wekaCluster-controller"),
		ExecService: exec.NewExecService(restConfig),
		KubeService: kubernetes.NewKubeService(mgr.GetClient()),
		config:      config,
	}

	return reconciler
}

func (r *TombstoneReconciller) RunGC(ctx context.Context) {
	if r.config.EnableTombstoneGc {
		go r.GCLoop(r.config)
	}
}

func (r *TombstoneReconciller) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	// check if object is being deleted, only then take action
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "TombstoneReconcile")
	defer end()

	ctx, cancel := context.WithTimeout(ctx, config.Config.Timeouts.ReconcileTimeout)
	defer cancel()

	logger.Debug("reconciling tombstone", "name", request.Name, "namespace", request.Namespace)
	tombstone := &wekav1alpha1.Tombstone{}
	err := r.Client.Get(ctx, request.NamespacedName, tombstone)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}
	nodename := tombstone.Spec.NodeAffinity
	node := corev1.Node{}
	err = r.Client.Get(ctx, client.ObjectKey{Name: string(nodename)}, &node)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if r.config.DeleteOnNodeMissing {
				logger.Info("node is not a member of cluster, removing tombstone", "node", nodename)
				err = r.removeFinalizer(ctx, tombstone)
				if err != nil {
					logger.Error(err, "Failed to remove finalizer", "nodename", nodename, "finalizer", WekaFinalizer)
					return reconcile.Result{}, err
				}
			} else {
				logger.Info("node is not a member of cluster, keeping tombstone", "node", nodename)
				return reconcile.Result{}, nil
			}
		}
		logger.Error(err, "Failed to locate tombstone node", "nodename", nodename)
		return reconcile.Result{}, err
	}

	err = r.ensureFinalizer(ctx, tombstone)
	if err != nil {
		return reconcile.Result{}, err
	}

	if tombstone.DeletionTimestamp != nil {
		// create a new Job spec and schedule to delete the tombstone on disk
		job, err := r.GetDeletionJob(tombstone)
		if err != nil {
			logger.Info("error getting deletion job", "error", err)
			return reconcile.Result{RequeueAfter: time.Minute}, nil
		}
		err = r.Client.Create(ctx, job)
		if err != nil {
			if client.IgnoreAlreadyExists(err) != nil {
				return reconcile.Result{}, err
			}
		}
		// poll the job, if finished sucsefully - we can proceed
		err = r.Client.Get(ctx, client.ObjectKeyFromObject(job), job)
		if err != nil {
			return reconcile.Result{}, err
		}
		if job.Status.Succeeded > 0 {
			// delete finalizer
			changed := controllerutil.RemoveFinalizer(tombstone, WekaFinalizer)
			if changed {
				err = r.Client.Update(ctx, tombstone)
				if err != nil {
					return reconcile.Result{}, err
				}
			}
			return reconcile.Result{}, nil
		}

		if job.CreationTimestamp.Add(3 * time.Minute).Before(time.Now()) {
			// job seems stuck, do we have node affinity and does node still exists?
			if tombstone.Spec.NodeAffinity != "" {
				_, err := r.KubeService.GetNode(ctx, types.NodeName(tombstone.Spec.NodeAffinity))
				if apierrors.IsNotFound(err) {
					// node does not exist, we can remove the job and finalizer
					// cannot use job removal by reference due to provisioning of job in non-cluster namespace
					err = r.Client.Delete(ctx, job)
					if err != nil {
						if !apierrors.IsNotFound(err) {
							return reconcile.Result{}, err
						}
					}
					changed := controllerutil.RemoveFinalizer(tombstone, WekaFinalizer)
					if changed {
						err = r.Client.Update(ctx, tombstone)
						if err != nil {
							return reconcile.Result{}, err
						}
					}
					return reconcile.Result{}, nil
				}
			}
		}

		return reconcile.Result{
			Requeue:      true,
			RequeueAfter: 3 * time.Second,
		}, nil
	}

	return reconcile.Result{}, nil
}

func getWekaContainerByUUID(ctx context.Context, r client.Client, namespace string, uuid string) (*wekav1alpha1.WekaContainer, error) {
	wekaContainerList := &wekav1alpha1.WekaContainerList{}
	listOpts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingFields{"metadata.uid": uuid},
	}
	err := r.List(ctx, wekaContainerList, listOpts...)
	if err != nil {
		return nil, err
	}

	if len(wekaContainerList.Items) == 0 {
		return nil, nil
	}

	// Assuming UUIDs are unique, return the first match
	return &wekaContainerList.Items[0], nil
}

func (r *TombstoneReconciller) GetDeletionJob(tombstone *wekav1alpha1.Tombstone) (*v1.Job, error) {
	_, logger, end := instrumentation.GetLogSpan(context.Background(), "GetDeletionJob")
	defer end()
	serviceAccountName := config.Config.MaintenanceSaName

	jobName := "weka-tombstone-delete-" + string(tombstone.UID)
	logger.Debug("fetching job", "jobName", jobName)
	if tombstone.Spec.CrId == "" {
		return nil, fmt.Errorf("tombstone CR ID is empty, refusing removal")
	}
	var ttl int32 = 60
	namespace, err := util.GetPodNamespace()
	if err != nil {
		return nil, err
	}
	//NOTE: Maybe could use WekaContainer, that will use alternative image, and container controller will close the loop in a smarter way

	if tombstone.Spec.CrType == "WekaContainer" {
		wekaContainer, err := getWekaContainerByUUID(context.Background(), r.Client, tombstone.Namespace, tombstone.Spec.CrId)
		if err != nil {
			return nil, err
		}
		if wekaContainer != nil {
			return nil, fmt.Errorf("weka container still exists, refusing removal")
		}
	}

	persistencePath := tombstone.Spec.PersistencePath
	if persistencePath == "" {
		// we assume that new version of tombstone will always have the persistencePath.
		// if not, we will use the default path for pre-existing tombstones, being the "plain kubernetes" one
		persistencePath = wekav1alpha1.PersistencePathBase + "/containers"
	}

	maintenanceImage := config.Config.MaintenanceImage
	maintenanceImagePullSecret := config.Config.MaintenanceImagePullSecret

	if tombstone.Spec.CrId == "" {
		return nil, fmt.Errorf("tombstone CR ID is empty, refusing removal")
	}

	job := &v1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace, // since we access all containers, must put this into the same namespace from access sharing perspective
			Labels:    tombstone.ObjectMeta.GetLabels(),
		},
		Spec: v1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Tolerations:        tombstone.Spec.Tolerations,
					ServiceAccountName: serviceAccountName,
					Containers: []corev1.Container{
						{
							Name:  "delete-tombstone",
							Image: maintenanceImage,
							Command: []string{
								"sh",
								"-c",
								fmt.Sprintf("echo 'Deleting tombstone %s/%s' && rm -rf ", persistencePath, tombstone.Spec.CrId) +
									fmt.Sprintf("%s/%s 2>&1", persistencePath, tombstone.Spec.CrId) +
									" && echo 'Tombstone deleted' || echo 'Tombstone not found' && sleep 5 && exit 0",
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "weka-containers-persistency",
									MountPath: persistencePath,
								},
							},
						},
					},
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Volumes: []corev1.Volume{
						{
							Name: "weka-containers-persistency",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: persistencePath,
									Type: &[]corev1.HostPathType{corev1.HostPathDirectoryOrCreate}[0],
								},
							},
						},
					},
				},
			},
		},
	}

	if maintenanceImagePullSecret != "" {
		job.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{
			{
				Name: maintenanceImagePullSecret,
			},
		}
	}
	if tombstone.Spec.NodeAffinity != "" {
		job.Spec.Template.Spec.Affinity = &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "kubernetes.io/hostname",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{string(tombstone.Spec.NodeAffinity)},
								},
							},
						},
					},
				},
			},
		}
	}
	//err = controllerutil.SetOwnerReference(tombstone, job, r.Scheme)
	return job, nil
}

func (r *TombstoneReconciller) ensureFinalizer(ctx context.Context, tombstone *wekav1alpha1.Tombstone) error {
	if !slices.Contains(tombstone.Finalizers, WekaFinalizer) {
		tombstone.Finalizers = append(tombstone.Finalizers, WekaFinalizer)
		err := r.Client.Update(ctx, tombstone)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *TombstoneReconciller) removeFinalizer(ctx context.Context, tombtsone *wekav1alpha1.Tombstone) error {
	if slices.Contains(tombtsone.Finalizers, WekaFinalizer) {
		var newFinalizers []string
		for _, finalizer := range tombtsone.Finalizers {
			if finalizer == WekaFinalizer {
				continue
			}
			newFinalizers = append(newFinalizers, finalizer)
		}
		tombtsone.Finalizers = newFinalizers
		err := r.Client.Update(ctx, tombtsone)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *TombstoneReconciller) GCLoop(config TombstoneConfig) {
	for {
		ctx := context.Background()
		//getlogspan
		_ = r.GC(ctx)
		time.Sleep(config.TombstoneGcInterval)
	}
}

func (r *TombstoneReconciller) GC(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "TombstoneGC")
	defer end()

	tombstones := &wekav1alpha1.TombstoneList{}
	err := r.Client.List(ctx, tombstones)
	if err != nil {
		return err
	}

	for _, tombstone := range tombstones.Items {
		if tombstone.DeletionTimestamp == nil {
			if tombstone.CreationTimestamp.Add(r.config.TombstoneExpiration).Before(time.Now()) {
				err := r.Client.Delete(ctx, &tombstone)
				if err != nil {
					if apierrors.IsNotFound(err) {
						continue
					}
					logger.Error(err, "error deleting tombstone")
				}
			}
		}
	}
	return nil
}
