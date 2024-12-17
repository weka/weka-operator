package operations

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
	v1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type CleanupPersistentDirPayload struct {
	NodeName        v1alpha1.NodeName `json:"node_name"`
	ContainerId     string            `json:"container_id"`
	PersistencePath string            `json:"persistence_path"`
}

type CleanupPersistentDirOperation struct {
	client      client.Client
	kubeService kubernetes.KubeService
	scheme      *runtime.Scheme
	payload     *CleanupPersistentDirPayload
	image       string
	pullSecret  string
	job         *v1.Job // internal field
	ownerRef    client.Object
	mgr         ctrl.Manager
	tolerations []corev1.Toleration
}

func NewCleanupPersistentDirOperation(mgr ctrl.Manager, payload *CleanupPersistentDirPayload, ownerRef client.Object, ownerDetails v1alpha1.WekaContainerDetails) *CleanupPersistentDirOperation {
	return &CleanupPersistentDirOperation{
		mgr:         mgr,
		client:      mgr.GetClient(),
		kubeService: kubernetes.NewKubeService(mgr.GetClient()),
		scheme:      mgr.GetScheme(),
		payload:     payload,
		image:       ownerDetails.Image,
		pullSecret:  ownerDetails.ImagePullSecret,
		tolerations: ownerDetails.Tolerations,
		ownerRef:    ownerRef,
	}
}

func (o *CleanupPersistentDirOperation) AsStep() lifecycle.Step {
	return lifecycle.Step{
		Name: "CleanupPersistentDir",
		Run:  AsRunFunc(o),
	}
}

func (o *CleanupPersistentDirOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		{Name: "GetJob", Run: o.GetJob},
		{
			Name:                      "EnsureJob",
			Run:                       o.EnsureJob,
			Predicates:                lifecycle.Predicates{o.HasNoJob},
			ContinueOnPredicatesFalse: true,
		},
		{Name: "PollStatus", Run: o.PollStatus},
		{Name: "DeleteJob", Run: o.DeleteJob},
	}
}

func (o *CleanupPersistentDirOperation) EnsureJob(ctx context.Context) error {
	if o.job != nil {
		return nil
	}

	ttl := int32(60)
	jobName := o.getJobName()
	namespace, err := util.GetPodNamespace()
	if err != nil {
		return errors.Wrap(err, "failed to get pod namespace")
	}

	serviceAccountName := config.Config.MaintenanceSaName
	maintenanceImage := config.Config.MaintenanceImage
	maintenanceImagePullSecret := config.Config.MaintenanceImagePullSecret
	hostPathType := corev1.HostPathDirectoryOrCreate

	persistencePath := o.payload.PersistencePath
	containerId := o.payload.ContainerId
	nodeSelector := map[string]string{
		"kubernetes.io/hostname": string(o.payload.NodeName),
	}

	if containerId == "" || persistencePath == "" {
		return errors.New("containerId and persistencePath must be set")
	}

	containerDataPath := fmt.Sprintf("%s/%s", persistencePath, containerId)

	job := &v1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
			Labels:    o.ownerRef.GetLabels(),
		},
		Spec: v1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector:       nodeSelector,
					Tolerations:        o.tolerations,
					ServiceAccountName: serviceAccountName,
					Containers: []corev1.Container{
						{
							Name:  "cleanup-persistent-dir",
							Image: maintenanceImage,
							Command: []string{
								"sh",
								"-c",
								fmt.Sprintf(
									"echo 'Cleanup container data %s/*' && rm -rf %s/* 2>&1 ", containerDataPath, containerDataPath,
								) + "&& echo 'Cleanup done' || { echo 'Cannot delete specified dir'; sleep 5; exit 1; }",
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "weka-container-data",
									MountPath: containerDataPath,
								},
							},
						},
					},
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Volumes: []corev1.Volume{
						{
							Name: "weka-container-data",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: containerDataPath,
									Type: &hostPathType,
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

	err = o.client.Create(ctx, job)
	if err != nil {
		return err
	}

	o.job = job
	return nil
}

func (o *CleanupPersistentDirOperation) getJobName() string {
	return fmt.Sprintf("weka-cleanup-container-%s", o.payload.ContainerId)
}

func (o *CleanupPersistentDirOperation) GetJob(ctx context.Context) error {
	name := o.getJobName()

	namespace, err := util.GetPodNamespace()
	if err != nil {
		return errors.Wrap(err, "failed to get pod namespace")
	}

	existing := &v1.Job{}
	err = o.client.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}, existing)
	if err != nil && apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return errors.Wrap(err, "Failed to get existing job")
	}
	o.job = existing
	return nil
}

func (o *CleanupPersistentDirOperation) DeleteJob(ctx context.Context) error {
	if o.job != nil {
		opts := []client.DeleteOption{
			// delete Completed pod created by the job
			client.PropagationPolicy(metav1.DeletePropagationBackground),
		}
		err := o.client.Delete(ctx, o.job, opts...)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	o.job = nil
	return nil
}

func (o *CleanupPersistentDirOperation) PollStatus(ctx context.Context) error {
	if o.job.Status.Succeeded == 0 {
		return lifecycle.NewWaitError(errors.New("job is not done yet"))
	}
	return nil
}

func (o *CleanupPersistentDirOperation) GetJsonResult() string {
	return "{}"
}

func (o *CleanupPersistentDirOperation) HasNoJob() bool {
	return o.job == nil
}
