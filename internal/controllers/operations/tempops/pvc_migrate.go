package tempops

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
)

const (
	PvcMigrateJobTTL = 60 * 15 // 15 minutes
	SourceMountPath  = "/source"
	DestMountPath    = "/destination"
)

type PvcMigrateOperation struct {
	client               client.Client
	kubeService          kubernetes.KubeService
	scheme               *runtime.Scheme
	image                string
	pullSecret           string
	job                  *v1.Job // internal field
	container            *weka.WekaContainer
	mgr                  ctrl.Manager
	tolerations          []corev1.Toleration
	originalNodeSelector map[string]string
}

func NewPvcMigrateOperation(mgr ctrl.Manager, container *weka.WekaContainer) *PvcMigrateOperation {
	ownerDetails := *container.ToContainerDetails()
	return &PvcMigrateOperation{
		mgr:                  mgr,
		client:               mgr.GetClient(),
		kubeService:          kubernetes.NewKubeService(mgr.GetClient()),
		scheme:               mgr.GetScheme(),
		image:                ownerDetails.Image,
		pullSecret:           ownerDetails.ImagePullSecret,
		tolerations:          ownerDetails.Tolerations,
		container:            container,
		originalNodeSelector: container.Spec.NodeSelector,
	}
}

func (o *PvcMigrateOperation) AsStep() lifecycle.Step {
	return &lifecycle.SingleStep{
		Name: "PvcMigrate",
		Run:  operations.AsRunFunc(o),
	}
}

func (o *PvcMigrateOperation) GetSteps() []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SingleStep{Name: "GetJob", Run: o.GetJob},
		&lifecycle.SingleStep{
			Name:       "EnsureJob",
			Run:        o.EnsureJob,
			Predicates: lifecycle.Predicates{o.HasNoJob},
		},
		&lifecycle.SingleStep{Name: "PollStatus", Run: o.PollStatus},
		// No DeleteJob step, relying on TTLSecondsAfterFinished
	}
}

func (o *PvcMigrateOperation) getJobName() string {
	return fmt.Sprintf("weka-pvc-migrate-%s", o.container.UID)
}

func (o *PvcMigrateOperation) EnsureJob(ctx context.Context) error {
	if o.job != nil {
		return nil
	}

	ttl := int32(PvcMigrateJobTTL)
	jobName := o.getJobName()
	namespace, err := util.GetPodNamespace()
	if err != nil {
		return errors.Wrap(err, "failed to get pod namespace")
	}

	serviceAccountName := config.Config.MaintenanceSaName
	maintenanceImage := config.Config.MaintenanceImage
	maintenanceImagePullSecret := config.Config.MaintenanceImagePullSecret
	hostPathType := corev1.HostPathDirectoryOrCreate
	containerId := string(o.container.UID)

	// Node selector must target the specific node for hostPath volume
	nodeSelector := map[string]string{
		"kubernetes.io/hostname": string(o.container.GetNodeAffinity()),
	}

	// Source path within the PVC volume
	sourcePath := fmt.Sprintf("%s/containers/%s/", SourceMountPath, containerId)
	// Destination path within the hostPath volume
	destHostPathBase := "/opt/k8s-weka" // Typically /opt/weka/k8s-weka
	destPath := fmt.Sprintf("%s/containers/%s/", DestMountPath, containerId)

	// cp command: -a flag for archive mode (recursive, preserve links, attributes).
	// Copy contents of source directory into destination directory.
	// Create destination directory first.
	// Use 'cp -aT' to treat the source as a file/directory to copy *into* the destination,
	// preventing issues if sourcePath itself is a symlink.
	cpCommand := fmt.Sprintf("mkdir -p %s && cp -aT %s %s", destPath, sourcePath, destPath)
	command := []string{"sh", "-c", cpCommand}

	volumes := []corev1.Volume{
		{
			Name: "weka-container-data-pvc",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: o.container.Spec.PVC.Name,
				},
			},
		},
		{
			Name: "weka-container-data-host",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: destHostPathBase, // Mount the base host path
					Type: &hostPathType,
				},
			},
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "weka-container-data-pvc",
			MountPath: SourceMountPath, // Mount PVC at /source
		},
		{
			Name:      "weka-container-data-host",
			MountPath: DestMountPath, // Mount host path at /destination
		},
	}

	job := &v1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
			Labels:    o.container.GetLabels(),
		},
		Spec: v1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			BackoffLimit:            util.Int32Ref(3),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector:       nodeSelector,
					Tolerations:        resources.ConditionalExpandNoScheduleTolerations(o.tolerations, !config.Config.SkipAuxNoScheduleToleration),
					ServiceAccountName: serviceAccountName,
					Containers: []corev1.Container{
						{
							Name:         "pvc-migrate",
							Image:        maintenanceImage, // Use maintenance image which should have cp
							Command:      command,
							VolumeMounts: volumeMounts,
						},
					},
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Volumes:       volumes,
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

	// Set container as owner for garbage collection (though TTL handles job cleanup)
	// we expect such cluster to be in operator namespace, aligned with pvc namespace
	if err := controllerutil.SetControllerReference(o.container, job, o.scheme); err != nil {
		return errors.Wrap(err, "failed setting controller reference for pvc migrate job")
	}

	err = o.client.Create(ctx, job)
	if err != nil {
		// If job already exists (e.g., from a previous reconcile attempt), fetch it
		if apierrors.IsAlreadyExists(err) {
			return o.GetJob(ctx)
		}
		return errors.Wrap(err, "failed to create pvc migrate job")
	}

	o.job = job
	return nil
}

func (o *PvcMigrateOperation) GetJob(ctx context.Context) error {
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
	if err != nil {
		if apierrors.IsNotFound(err) {
			o.job = nil // Ensure job is nil if not found
			return nil
		}
		return errors.Wrap(err, "failed to get existing pvc migrate job")
	}
	o.job = existing
	return nil
}

func (o *PvcMigrateOperation) PollStatus(ctx context.Context) error {
	if o.job == nil {
		// Should have been created or fetched in previous steps
		return errors.New("job not found, cannot poll status")
	}
	// Refresh job status
	err := o.client.Get(ctx, client.ObjectKeyFromObject(o.job), o.job)
	if err != nil {
		return errors.Wrap(err, "failed to refresh job status")
	}

	if o.job.Status.Succeeded > 0 {
		// Job completed successfully
		return nil
	}

	if o.job.Status.Failed > 0 {
		// Job failed
		// TODO: Extract pod logs for better error reporting?
		return fmt.Errorf("pvc migrate job %s failed", o.job.Name)
	}

	// Job is still running or pending
	err = fmt.Errorf("pvc migrate job %s is not complete yet", o.job.Name)
	return lifecycle.NewWaitError(err)
}

func (o *PvcMigrateOperation) GetJsonResult() string {
	// This operation doesn't produce a JSON result for the owner status
	return "{}"
}

func (o *PvcMigrateOperation) HasNoJob() bool {
	return o.job == nil
}
