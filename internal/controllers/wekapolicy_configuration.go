package controllers

import (
	"context"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/weka/weka-operator/internal/services"
)

// configurationPolicyStatus marks a configuration policy as carrying live settings.
//
// Deliberately not "Done": DurationTillNext keys off that value and would then requeue the policy
// every spec.payload.interval forever, for a policy that never runs.
const configurationPolicyStatus = "Active"

// policyFailedStatus matches what the operation path writes via onFailure, so a policy that fails
// validation reads the same as one whose run failed.
const policyFailedStatus = "Failed"

// reconcileConfiguration handles a configuration policy, which holds operator-wide settings rather
// than an operation to perform. Other reconcilers read those settings on demand through
// services.ConfigurationCache, so there is nothing to execute here.
func (r *WekaPolicyReconciler) reconcileConfiguration(ctx context.Context, wekaPolicy *weka.WekaPolicy) (ctrl.Result, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "WekaPolicyReconcileConfiguration")
	defer logger.End()

	// The spec may have changed, so drop the cached copy and let the next reader re-read. This has
	// to happen before the early return below, or an edit to an already-Active policy would never
	// invalidate and would wait out the cache TTL.
	services.ConfigurationCache.Invalidate()

	// Only write when the value actually changes: an unconditional status update would trip this
	// controller's own watch and spin.
	if wekaPolicy.Status.Status == configurationPolicyStatus {
		return ctrl.Result{}, nil
	}

	wekaPolicy.Status.Status = configurationPolicyStatus
	if wekaPolicy.Status.LastRunTime.IsZero() {
		// lastRunTime is required by the CRD and a zero metav1.Time marshals to null, which the
		// API server rejects. A configuration policy never runs, so backdate it the same way the
		// operation path does; DurationTillNext only schedules on status "Done", so this does not
		// cause a requeue.
		wekaPolicy.Status.LastRunTime = metav1.NewTime(time.Now().Add(-time.Hour))
	}
	if err := r.Status().Update(ctx, wekaPolicy); err != nil {
		logger.Error(err, "Failed to update configuration WekaPolicy status")
		return ctrl.Result{}, err
	}

	logger.Info("Configuration policy active")
	return ctrl.Result{}, nil
}

// reportPolicyFailure records a reconcile failure on the policy itself, so it is visible through
// kubectl rather than only in the operator log. Status write failures are logged and swallowed:
// the caller is already returning the underlying error.
func (r *WekaPolicyReconciler) reportPolicyFailure(ctx context.Context, wekaPolicy *weka.WekaPolicy, cause error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "WekaPolicyReportFailure")
	defer logger.End()

	if wekaPolicy.Status.Status == policyFailedStatus && wekaPolicy.Status.LastResult == cause.Error() {
		return
	}

	wekaPolicy.Status.Status = policyFailedStatus
	wekaPolicy.Status.LastResult = cause.Error()
	if wekaPolicy.Status.LastRunTime.IsZero() {
		// lastRunTime is required by the CRD and a zero metav1.Time marshals to null, which the
		// API server rejects
		wekaPolicy.Status.LastRunTime = metav1.NewTime(time.Now().Add(-time.Hour))
	}

	if err := r.Status().Update(ctx, wekaPolicy); err != nil {
		logger.Error(err, "Failed to record WekaPolicy failure status")
	}
}
