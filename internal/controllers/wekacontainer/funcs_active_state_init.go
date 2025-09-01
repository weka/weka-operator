package wekacontainer

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/pkg/util"
)

func (r *containerReconcilerLoop) initState(ctx context.Context) error {
	if r.container.Status.Conditions == nil {
		r.container.Status.Conditions = []metav1.Condition{}
	}

	if r.container.Status.PrinterColumns == nil {
		r.container.Status.PrinterColumns = &weka.ContainerPrinterColumns{}
	}

	changed := false

	if r.container.Status.Status == "" {
		r.container.Status.Status = weka.Init
		changed = true
	}

	if r.HasNodeAffinity() && r.container.Status.PrinterColumns.NodeAffinity == "" {
		r.container.Status.PrinterColumns.NodeAffinity = string(r.container.GetNodeAffinity())
		changed = true
	}

	// save printed management IPs if not set (for the back-compatibility with "single" managementIP)
	if r.container.Status.GetPrinterColumns().ManagementIPs == "" && len(r.container.Status.GetManagementIps()) > 0 {
		r.container.Status.PrinterColumns.SetManagementIps(r.container.Status.GetManagementIps())
		changed = true
	}

	if changed {
		if err := r.Status().Update(ctx, r.container); err != nil {
			return errors.Wrap(err, "Failed to update status")
		}
	}

	return nil
}

func (r *containerReconcilerLoop) checkTolerations(ctx context.Context) error {
	ignoredTaints := config.Config.TolerationsMismatchSettings.GetIgnoredTaints()

	notTolerated := !util.CheckTolerations(r.node.Spec.Taints, r.container.Spec.Tolerations, ignoredTaints)

	if notTolerated == r.container.Status.NotToleratedOnReschedule {
		return nil
	}

	r.container.Status.NotToleratedOnReschedule = notTolerated
	return r.Status().Update(ctx, r.container)
}

func (r *containerReconcilerLoop) ensureFinalizer(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container

	if ok := controllerutil.AddFinalizer(container, resources.WekaFinalizer); !ok {
		return nil
	}

	logger.Info("Adding Finalizer for weka container")
	err := r.Update(ctx, container)
	if err != nil {
		logger.Error(err, "Failed to update wekaCluster with finalizer")
		return err
	}

	return nil
}

func (r *containerReconcilerLoop) ensureBootConfigMapInTargetNamespace(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureBootConfigMapInTargetNamespace")
	defer end()

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
	err = r.Get(ctx, client.ObjectKey{Namespace: r.container.Namespace, Name: bootScriptConfigName}, bootScripts)
	if err != nil {
		if apierrors.IsNotFound(err) {
			bootScripts.Namespace = r.container.Namespace
			bootScripts.Name = bootScriptConfigName
			bootScripts.Data = bundledConfigMap.Data
			if err := r.Create(ctx, bootScripts); err != nil {
				if apierrors.IsAlreadyExists(err) {
					logger.Info("Boot scripts config map already exists in designated namespace")
				} else {
					logger.Error(err, "Error creating boot scripts config map")
				}
			}
			logger.Info("Created boot scripts config map in designated namespace")
		}
	}

	if !util.IsEqualConfigMapData(bootScripts, bundledConfigMap) {
		bootScripts.Data = bundledConfigMap.Data
		if err := r.Update(ctx, bootScripts); err != nil {
			logger.Error(err, "Error updating boot scripts config map")
			return err
		}
		logger.InfoWithStatus(codes.Ok, "Updated and reconciled boot scripts config map in designated namespace")

	}

	return nil
}

func (r *containerReconcilerLoop) updatePodMetadataOnChange(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "updatePodMetadataOnChange")
	defer end()

	pod := r.pod
	newLabels := resources.LabelsForWekaPod(r.container)
	newAnnotations := resources.AnnotationsForWekaPod(r.container.GetAnnotations(), r.pod.GetAnnotations())
	pod.SetLabels(newLabels)
	pod.SetAnnotations(newAnnotations)

	logger.Info("Updating pod metadata", "new_labels", newLabels, "new_annotations", newAnnotations)

	if err := r.Update(ctx, pod); err != nil {
		return fmt.Errorf("failed to update pod labels: %w", err)
	}
	r.pod = pod

	return nil
}

func (r *containerReconcilerLoop) podMetadataChanged() bool {
	oldLabels := r.pod.GetLabels()
	newLabels := resources.LabelsForWekaPod(r.container)

	if !util.NewHashableMap(newLabels).Equals(util.NewHashableMap(oldLabels)) {
		return true
	}

	oldAnnotations := r.pod.GetAnnotations()
	newAnnotations := resources.AnnotationsForWekaPod(r.container.GetAnnotations(), oldAnnotations)

	return !util.NewHashableMap(newAnnotations).Equals(util.NewHashableMap(oldAnnotations))
}

func (r *containerReconcilerLoop) dropStopAttemptRecord(ctx context.Context) error {
	// clear r.container.Status.Timestamps[TimestampStopAttempt
	if r.container.Status.Timestamps == nil {
		return nil
	}
	if _, ok := r.container.Status.Timestamps[string(weka.TimestampStopAttempt)]; !ok {
		return nil
	}
	delete(r.container.Status.Timestamps, string(weka.TimestampStopAttempt))
	return r.Status().Update(ctx, r.container)
}
