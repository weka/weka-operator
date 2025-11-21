package operations

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
)

type CleanupPersistentDirPayload struct {
	NodeName        v1alpha1.NodeName `json:"node_name"`
	ContainerId     string            `json:"container_id"`
	PersistencePath string            `json:"persistence_path"`
	RunPrivileged   bool              `json:"run_privileged"` // whether to run the cleanup job in privileged mode
}

type CleanupPersistentDirOperation struct {
	client               client.Client
	kubeService          kubernetes.KubeService
	scheme               *runtime.Scheme
	payload              *CleanupPersistentDirPayload
	image                string
	pullSecret           string
	job                  *v1.Job // internal field
	container            *v1alpha1.WekaContainer
	mgr                  ctrl.Manager
	tolerations          []corev1.Toleration
	originalNodeSelector map[string]string
}

func NewCleanupPersistentDirOperation(mgr ctrl.Manager, payload *CleanupPersistentDirPayload, container *v1alpha1.WekaContainer, ownerDetails v1alpha1.WekaOwnerDetails, originalNodeSelector map[string]string) *CleanupPersistentDirOperation {
	return &CleanupPersistentDirOperation{
		mgr:                  mgr,
		client:               mgr.GetClient(),
		kubeService:          kubernetes.NewKubeService(mgr.GetClient()),
		scheme:               mgr.GetScheme(),
		payload:              payload,
		image:                ownerDetails.Image,
		pullSecret:           ownerDetails.ImagePullSecret,
		tolerations:          ownerDetails.Tolerations,
		container:            container,
		originalNodeSelector: originalNodeSelector,
	}
}

func (o *CleanupPersistentDirOperation) AsStep() lifecycle.Step {
	return &lifecycle.SimpleStep{
		Name: "CleanupPersistentDir",
		Run:  AsRunFunc(o),
	}
}

func (o *CleanupPersistentDirOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SimpleStep{Name: "GetJob", Run: o.GetJob},
		&lifecycle.SimpleStep{
			Name:       "EnsureJob",
			Run:        o.EnsureJob,
			Predicates: lifecycle.Predicates{o.HasNoJob},
		},
		&lifecycle.SimpleStep{Name: "PollStatus", Run: o.PollStatus},
		&lifecycle.SimpleStep{Name: "DeleteJob", Run: o.DeleteJob},
	}
}

func (o *CleanupPersistentDirOperation) EnsureJob(ctx context.Context) error {
	if o.job != nil {
		return nil
	}

	ttl := int32(60 * 15) // 15 minutes
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

	nodeSelector := o.originalNodeSelector
	if o.container.Spec.PVC != nil {
		if len(nodeSelector) == 0 {
			nodeSelector = map[string]string{}
		}
	} else {
		if config.Config.LocalDataPvc != "" {
			return errors.New("race, container was not updated for on-container-spec pvc") // TODO: Remove after migrations
		}
		nodeSelector = map[string]string{
			"kubernetes.io/hostname": string(o.payload.NodeName),
		}
	}

	if containerId == "" || persistencePath == "" {
		return errors.New("containerId and persistencePath must be set")
	}

	containerDataPath := fmt.Sprintf("%s/%s", persistencePath, containerId)
	var mountPath, subPath string
	if o.container.Spec.PVC != nil {
		// If PVC is specified, mount at /opt/k8s-weka/containers and clean specific container dir under it
		mountPath = persistencePath
		subPath = "containers"
	} else {
		if config.Config.LocalDataPvc != "" {
			return errors.New("race, container was not updated for on-container-spec pvc") // TODO: Remove after migrations
		}
		// Otherwise use the container-specific path for both mounting and cleanup
		mountPath = containerDataPath
	}

	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "weka-container-data",
			MountPath: mountPath,
			SubPath:   subPath,
		},
	}
	volumes := []corev1.Volume{}

	if o.container.Spec.PVC != nil {
		// If PVC is specified, mount it at /opt/k8s-weka
		volumes = append(volumes, corev1.Volume{
			Name: "weka-container-data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: o.container.Spec.PVC.Name,
				},
			},
		})
	} else {
		// Otherwise use hostPath
		volumes = append(volumes, corev1.Volume{
			Name: "weka-container-data",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: containerDataPath,
					Type: &hostPathType,
				},
			},
		})
	}

	job := &v1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
			Labels:    o.container.GetLabels(),
		},
		Spec: v1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector:       nodeSelector,
					Tolerations:        resources.ConditionalExpandNoScheduleTolerations(o.tolerations, !config.Config.SkipAuxNoScheduleToleration),
					ServiceAccountName: serviceAccountName,
					Containers: []corev1.Container{
						{
							Name:         "cleanup-persistent-dir",
							Image:        maintenanceImage,
							Command:      []string{"sh", "-c"},
							Args:         []string{o.getRmCommand(containerDataPath)},
							VolumeMounts: volumeMounts,
						},
					},
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Volumes:       volumes,
				},
			},
		},
	}

	if o.payload.RunPrivileged {
		job.Spec.Template.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{
			Privileged: &[]bool{true}[0],
		}
	}

	if o.payload.NodeName != "" {
		job.Spec.Template.Spec.NodeName = string(o.payload.NodeName)
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
		err := fmt.Errorf("job is not done yet - %s", o.job.Name)
		return lifecycle.NewWaitError(err)
	}
	return nil
}

func (o *CleanupPersistentDirOperation) GetJsonResult() string {
	return "{}"
}

func (o *CleanupPersistentDirOperation) HasNoJob() bool {
	return o.job == nil
}

func (o *CleanupPersistentDirOperation) getRmCommand(containerDataPath string) string {
	var rmSuffix string
	if o.container.Spec.PVC == nil {
		rmSuffix = "/*" // remove only the content of the directory
	} else {
		rmSuffix = "" // remove the entire directory
	}
	deletePath := fmt.Sprintf("%s%s", containerDataPath, rmSuffix)
	return fmt.Sprintf("echo 'Cleanup container data %s' && rm -rf %s 2>&1 && echo 'Cleanup done' || { echo 'Cannot delete specified dir'; sleep 5; exit 1; }", deletePath, deletePath)
}
