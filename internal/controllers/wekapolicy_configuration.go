package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/pkg/util"
)

// configurationPolicyStatus marks a configuration policy as carrying live settings.
//
// Deliberately not "Done": DurationTillNext keys off that value and would then requeue the policy
// every spec.payload.interval forever, for a policy that never runs.
const configurationPolicyStatus = "Active"

// policyFailedStatus matches what the operation path writes via onFailure, so a policy that fails
// validation reads the same as one whose run failed.
const policyFailedStatus = "Failed"

// configurationPolicyIgnoredStatus marks a well-formed configuration policy that sits outside the
// operator namespace. Not "Failed": nothing is wrong with the object, it is simply in a place
// these install-wide settings are not read from.
const configurationPolicyIgnoredStatus = "Ignored"

// reconcileConfiguration handles a configuration policy, which holds operator-wide settings rather
// than an operation to perform. Other reconcilers read those settings on demand through
// services.GetSettings, so there is nothing to execute here.
func (r *WekaPolicyReconciler) reconcileConfiguration(ctx context.Context, wekaPolicy *weka.WekaPolicy) (ctrl.Result, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "WekaPolicyReconcileConfiguration")
	defer logger.End()

	// These settings are read from the operator namespace only, so a policy anywhere else is
	// never consulted. Say so on the object instead of reporting Active, which would read as
	// "in effect" while the setting is silently ignored.
	if operatorNamespace, nsErr := util.GetPodNamespace(); nsErr != nil {
		// Falling through is deliberate: resolution needs the same namespace, so it fails too
		// and the policy is reported Failed below.
		logger.Error(nsErr, "Cannot resolve the operator namespace")
	} else if wekaPolicy.Namespace != operatorNamespace {
		return r.ignoreConfiguration(ctx, wekaPolicy, operatorNamespace)
	}

	// The spec may have changed, so re-resolve now rather than leaving readers on the previous
	// value until the background refresh. This has to happen before the early return below, or an
	// edit to an already-Active policy would never trigger it.
	if refreshErr := services.RefreshSettings(ctx, r.Client); refreshErr != nil {
		r.reportPolicyFailure(ctx, wekaPolicy, refreshErr)
		return ctrl.Result{}, nil
	}

	// Only write when something actually changes: an unconditional status update would trip this
	// controller's own watch and spin. lastResult is part of that comparison because a policy
	// that recovers from Failed or Ignored still carries the reason it stopped being usable, and
	// "Active" next to a failure message reads as a contradiction.
	if wekaPolicy.Status.Status == configurationPolicyStatus && wekaPolicy.Status.LastResult == "" {
		return ctrl.Result{}, nil
	}

	wekaPolicy.Status.Status = configurationPolicyStatus
	wekaPolicy.Status.LastResult = ""
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

// ignoreConfiguration marks a configuration policy that lives outside the operator namespace. It
// returns no error: retrying cannot change the namespace, and an edit to the object produces its
// own watch event.
func (r *WekaPolicyReconciler) ignoreConfiguration(ctx context.Context, wekaPolicy *weka.WekaPolicy, operatorNamespace string) (ctrl.Result, error) {
	reason := fmt.Sprintf("configuration is install-wide and is only read from the operator namespace %q", operatorNamespace)

	if wekaPolicy.Status.Status == configurationPolicyIgnoredStatus && wekaPolicy.Status.LastResult == reason {
		return ctrl.Result{}, nil
	}

	wekaPolicy.Status.Status = configurationPolicyIgnoredStatus
	wekaPolicy.Status.LastResult = reason
	if wekaPolicy.Status.LastRunTime.IsZero() {
		wekaPolicy.Status.LastRunTime = metav1.NewTime(time.Now().Add(-time.Hour))
	}

	if err := r.Status().Update(ctx, wekaPolicy); err != nil {
		return ctrl.Result{}, err
	}

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

	if wekaPolicy.Spec.Payload.Configuration != nil {
		_ = services.RefreshSettings(ctx, r.Client) //nolint:errcheck // the failure is already on the object
	}
}
