package wekacluster

import (
	"context"
	"strings"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1/condition"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/services/discovery"
)

const licenseSecretKey = "license"

func (r *wekaClusterReconcilerLoop) HasLicenseSecret() bool {
	return r.cluster.Spec.LicenseSecret != ""
}

func (r *wekaClusterReconcilerLoop) EnsureLicense(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "EnsureLicense")
	defer logger.End()

	secretName := r.cluster.Spec.LicenseSecret
	secret := &corev1.Secret{}
	if err := r.getClient().Get(ctx, client.ObjectKey{Name: secretName, Namespace: r.cluster.Namespace}, secret); err != nil {
		return r.licenseFailed(ctx, errors.Wrapf(err, "failed to get license secret %q", secretName))
	}
	license := strings.TrimSpace(string(secret.Data[licenseSecretKey]))
	if license == "" {
		return r.licenseFailed(ctx, errors.Errorf("license secret %q has no %q key", secretName, licenseSecretKey))
	}

	// UID is included so a deleted and recreated Secret is re-applied.
	version := string(secret.UID) + "/" + secret.ResourceVersion
	if version == getConditionConfigHash(r.cluster.Status.Conditions, condition.CondLicenseApplied) {
		return nil
	}

	container := discovery.SelectActiveContainer(r.containers)
	if container == nil {
		return r.licenseFailed(ctx, errors.New("no active container found to apply license"))
	}
	executor, err := r.ExecService.GetExecutor(ctx, container)
	if err != nil {
		return r.licenseFailed(ctx, errors.Wrap(err, "failed to get executor"))
	}
	// stderr is dropped: weka may echo the license back in its error output.
	if _, _, err := executor.ExecSensitive(ctx, "ApplyLicense", []string{"weka", "cluster", "license", "set", license}); err != nil {
		return r.licenseFailed(ctx, errors.Wrap(err, "failed to apply license"))
	}

	meta.SetStatusCondition(&r.cluster.Status.Conditions, metav1.Condition{
		Type:    condition.CondLicenseApplied,
		Status:  metav1.ConditionTrue,
		Reason:  "Applied",
		Message: version,
	})
	if err := r.getClient().Status().Update(ctx, r.cluster); err != nil {
		return errors.Wrap(err, "failed to update LicenseApplied condition")
	}
	logger.Info("License applied", "secret", secretName, "version", version)
	return nil
}

func (r *wekaClusterReconcilerLoop) licenseFailed(ctx context.Context, err error) error {
	_ = r.RecordEvent(corev1.EventTypeWarning, "LicenseApplyFailed", consts.ActionApplyLicense, err.Error()) //nolint:errcheck // error is intentionally ignored
	meta.SetStatusCondition(&r.cluster.Status.Conditions, metav1.Condition{
		Type:    condition.CondLicenseApplied,
		Status:  metav1.ConditionFalse,
		Reason:  "ApplyFailed",
		Message: err.Error(),
	})
	if updateErr := r.getClient().Status().Update(ctx, r.cluster); updateErr != nil {
		return errors.Wrapf(err, "also failed to update LicenseApplied condition: %v", updateErr)
	}
	return err
}
