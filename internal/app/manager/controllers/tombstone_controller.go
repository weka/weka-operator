package controllers

import (
	"context"
	"fmt"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	"github.com/weka/weka-operator/internal/app/manager/services"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/util"
	v1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"slices"
	"time"
)

type TombstoneReconciller struct {
	Client      client.Client
	Scheme      *runtime.Scheme
	Manager     ctrl.Manager
	Recorder    record.EventRecorder
	ExecService services.ExecService
}

func (r TombstoneReconciller) SetupWithManager(mgr ctrl.Manager, reconciler reconcile.Reconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.Tombstone{}).
		Complete(reconciler)
}

type TombstoneConfig struct {
	EnableTombstoneGc   bool
	TombstoneGcInterval time.Duration
	TombstoneExpiration time.Duration
}

func NewTombstoneController(mgr ctrl.Manager, config TombstoneConfig) *TombstoneReconciller {
	reconciler := &TombstoneReconciller{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		Manager:     mgr,
		Recorder:    mgr.GetEventRecorderFor("wekaCluster-controller"),
		ExecService: services.NewExecService(mgr),
	}

	if config.EnableTombstoneGc {
		go reconciler.GCLoop(config)
	}

	return reconciler
}

func (r TombstoneReconciller) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	// check if object is being deleted, only then take action
	tombstone := &wekav1alpha1.Tombstone{}
	err := r.Client.Get(ctx, request.NamespacedName, tombstone)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			return reconcile.Result{}, nil
		}
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
			return reconcile.Result{}, err
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

		return reconcile.Result{
			Requeue:      true,
			RequeueAfter: 3,
		}, nil
	}

	return reconcile.Result{}, nil
}

func (r TombstoneReconciller) GetDeletionJob(tombstone *wekav1alpha1.Tombstone) (*v1.Job, error) {
	if tombstone.Spec.CrId == "" {
		return nil, fmt.Errorf("tombstone CR ID is empty, refusing removal")
	}
	var ttl int32 = 60
	namespace, err := util.GetPodNamespace()
	if err != nil {
		return nil, err
	}
	//NOTE: Maybe could use WekaContainer, that will use alternative image, and container controller will close the loop in a smarter way

	job := &v1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "weka-tombstone-delete-" + string(tombstone.UID),
			Namespace: namespace, // since we access all containers, must put this into the same namespace from access sharing perspective
		},
		Spec: v1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "kubernetes.io/hostname",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{tombstone.Spec.NodeAffinity},
											},
										},
									},
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "delete-tombstone",
							Image: "busybox",
							Command: []string{
								"sh",
								"-c",
								"rm -rf " + fmt.Sprintf("%s/%s", resources.PersistentContainersLocation, tombstone.Spec.CrId),
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "weka-containers-persistency",
									MountPath: resources.PersistentContainersLocation,
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
									Path: resources.PersistentContainersLocation,
									Type: &[]corev1.HostPathType{corev1.HostPathDirectoryOrCreate}[0],
								},
							},
						},
					},
				},
			},
		},
	}
	return job, nil
}

func (r TombstoneReconciller) ensureFinalizer(ctx context.Context, tombstone *wekav1alpha1.Tombstone) error {
	if !slices.Contains(tombstone.Finalizers, WekaFinalizer) {
		tombstone.Finalizers = append(tombstone.Finalizers, WekaFinalizer)
		err := r.Client.Update(ctx, tombstone)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r TombstoneReconciller) GCLoop(config TombstoneConfig) {
	for {
		ctx := context.Background()
		//getlogspan
		_ = r.GC(ctx, config)
		time.Sleep(config.TombstoneGcInterval)
	}
}

func (r TombstoneReconciller) GC(ctx context.Context, config TombstoneConfig) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "TombstoneGC")
	defer end()

	tombstones := &wekav1alpha1.TombstoneList{}
	err := r.Client.List(ctx, tombstones)
	if err != nil {
		return err
	}

	for _, tombstone := range tombstones.Items {
		if tombstone.DeletionTimestamp == nil {
			if tombstone.CreationTimestamp.Add(config.TombstoneExpiration).Before(time.Now()) {
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
