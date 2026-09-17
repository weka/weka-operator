package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/pkg/util"
)

// WekaPolicyReconciler reconciles a WekaPolicy object
type WekaPolicyReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Mgr        ctrl.Manager
	RestClient rest.Interface
	Recorder   events.EventRecorder
}

func NewWekaPolicyController(mgr ctrl.Manager, restClient rest.Interface) *WekaPolicyReconciler {
	return &WekaPolicyReconciler{
		Mgr:        mgr,
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		RestClient: restClient,
		Recorder:   util.WrapEventRecorder(mgr.GetEventRecorder("wekaPolicy-controller"), mgr.GetScheme()),
	}
}

type policyLoop struct {
	Policy *weka.WekaPolicy
	Client client.Client
	Op     operations.Operation
}

//+kubebuilder:rbac:groups=weka.weka.io,resources=wekapolicies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekapolicies/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=weka.weka.io,resources=wekapolicies/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete

func (r *WekaPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "WekaPolicyReconcile", "namespace", req.Namespace, "object_name", req.Name)
	defer logger.End()

	ctx, cancel := context.WithTimeout(ctx, config.Config.Timeouts.ReconcileTimeout)
	defer cancel()

	// Fetch the WekaPolicy instance
	wekaPolicy := &weka.WekaPolicy{}
	err := r.Get(ctx, req.NamespacedName, wekaPolicy)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("WekaPolicy resource not found. Ignoring since object must be deleted")
			// The deleted object may have been the configuration policy. Pods keep the value
			// they were created with, so a stale read here outlives the refresh window.
			//
			// Read uncached: the informer can still hold the object we were just told is gone,
			// and resolving from that would re-apply the settings we are trying to drop.
			_ = services.RefreshSettings(ctx, r.Mgr.GetAPIReader()) //nolint:errcheck // logged by the resolver
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get WekaPolicy")
		return ctrl.Result{}, err
	}
	logger.Info("Reconciling WekaPolicy", "type", wekaPolicy.Spec.Type)

	// spec.type is optional: the type is derived from the payload when it is unset, and an
	// unusable payload combination is rejected here rather than guessed at.
	policyType, isConfiguration, typeErr := wekaPolicy.GetType()
	if typeErr != nil {
		// Recorded on the object rather than returned: only an edit can fix a payload the
		// resolver rejects, and that edit reconciles on its own.
		r.reportPolicyFailure(ctx, wekaPolicy, typeErr)
		return ctrl.Result{}, nil
	}

	// A configuration policy carries operator-wide settings that other reconcilers read on demand.
	// There is no run to schedule, so it branches out before the interval gate and the
	// Running/Done status machinery below.
	if isConfiguration {
		return r.reconcileConfiguration(ctx, wekaPolicy)
	}

	loop := &policyLoop{
		Policy: wekaPolicy,
		Client: r.Client,
	}

	if loop.DurationTillNext() > 0 {
		logger.Info("Policy not ready to run", "requeueAfter", loop.DurationTillNext())
		return ctrl.Result{RequeueAfter: loop.DurationTillNext()}, nil
	}

	if slices.Contains([]string{"Done", ""}, wekaPolicy.Status.Status) {
		wekaPolicy.Status.Status = "Running"
		if wekaPolicy.Status.LastRunTime.IsZero() {
			// put old time to avoid "invalidValue" error
			wekaPolicy.Status.LastRunTime = metav1.NewTime(time.Now().Add(-time.Hour))
		}
		err = r.Status().Update(ctx, wekaPolicy)
		if err != nil {
			logger.Error(err, "Failed to update WekaPolicy status")
			return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
		}
	}

	onSuccess := func(ctx context.Context) error {
		result := loop.Op.GetJsonResult()

		var resultMap map[string]interface{}
		if unmarshalErr := json.Unmarshal([]byte(result), &resultMap); unmarshalErr == nil {
			if reason, ok := resultMap["InProgress"].(bool); ok && !reason {
				wekaPolicy.Status.Status = "Expired"
			} else {
				wekaPolicy.Status.Status = "Done"
			}
		} else {
			wekaPolicy.Status.Status = "Done"
		}

		wekaPolicy.Status.LastResult = result
		wekaPolicy.Status.LastRunTime = metav1.Now()
		return r.Status().Update(ctx, wekaPolicy)
	}

	onFailure := func(ctx context.Context) error {
		wekaPolicy.Status.LastResult = loop.Op.GetJsonResult()
		wekaPolicy.Status.LastRunTime = metav1.Now()
		wekaPolicy.Status.Status = "Failed"
		return r.Status().Update(ctx, wekaPolicy)
	}

	ownerDetails := ownerDetailsFrom(ownerDetailsInput{
		Image:              wekaPolicy.Spec.Image,
		ImagePullSecret:    wekaPolicy.Spec.ImagePullSecret,
		Tolerations:        wekaPolicy.Spec.Tolerations,
		Labels:             wekaPolicy.GetLabels(),
		ServiceAccountName: wekaPolicy.Spec.ServiceAccountName,
	})

	switch policyType {
	case weka.WekaPolicyTypeSignDrives:
		signDrivesOp := operations.NewSignDrivesOperation(
			r.Mgr,
			wekaPolicy.Spec.Payload.SignDrives,
			wekaPolicy,
			ownerDetails,
			wekaPolicy.Status.Status,
			onSuccess,
			onFailure,
			false,
		)
		loop.Op = signDrivesOp
	case weka.WekaPolicyTypeDiscoverDrives:
		discoverDrivesOp := operations.NewDiscoverDrivesOperation(
			r.Mgr,
			wekaPolicy.Spec.Payload.DiscoverDrives,
			wekaPolicy,
			ownerDetails,
			wekaPolicy.Status.Status,
			onSuccess,
			false,
		)
		loop.Op = discoverDrivesOp
	case weka.WekaPolicyTypeEnsureNICs:
		ensureNICsOp := operations.NewEnsureNICsOperation(
			r.Mgr,
			wekaPolicy.Spec.Payload.EnsureNICs,
			wekaPolicy,
			ownerDetails,
			wekaPolicy.Status.Status,
			onSuccess,
		)
		loop.Op = ensureNICsOp
	case weka.WekaPolicyTypeEnableLocalDriversDistribution:
		if wekaPolicy.Spec.Payload.DriverDistPayload == nil {
			wekaPolicy.Spec.Payload.DriverDistPayload = &weka.DriverDistPayload{}
		}
		enableLocalDriversDistOp := operations.NewEnsureDistServiceOperation(
			r.Mgr,
			wekaPolicy.Spec.Payload.DriverDistPayload,
			wekaPolicy,
			ownerDetails,
			wekaPolicy.Status.Status,
			onSuccess,
			onFailure,
		)
		loop.Op = enableLocalDriversDistOp
	case weka.WekaPolicyTypeRemoteTracesSession:
		if wekaPolicy.Spec.Payload.RemoteTracesSession == nil {
			wekaPolicy.Spec.Payload.RemoteTracesSession = &weka.RemoteTracesSessionConfig{}
		}

		isExpired := wekaPolicy.Status.Status == "Expired"
		remoteTracesSessionOp := operations.NewMaintainTraceSession(
			r.Mgr,
			r.RestClient,
			wekaPolicy.Spec.Payload.RemoteTracesSession,
			wekaPolicy,
			ownerDetails,
			nil,
			onSuccess,
			onFailure,
			isExpired,
		)
		loop.Op = remoteTracesSessionOp
	case weka.WekaPolicyTypeCleanStaleVirtualDrives:
		staleVidsOp := operations.NewStaleVirtualDrivesOperation(
			r.Mgr,
			wekaPolicy.Spec.Payload.CleanStaleVirtualDrives,
			wekaPolicy,
			r.Recorder,
			nil, // policy: each Interval run is its own cycle; the gate spans runs via LastResult
			onSuccess,
		)
		loop.Op = staleVidsOp
	default:
		return ctrl.Result{}, fmt.Errorf("unknown policy type: %s", policyType)
	}

	steps := loop.Op.GetSteps()

	stepsEngine := lifecycle.StepsEngine{
		Steps: steps,
	}

	result, err := stepsEngine.RunAsReconcilerResponse(ctx)
	if err != nil {
		logger.Error(err, "Error processing policy")
		return result, err
	}

	// If the policy is done, requeue after the specified interval
	if loop.DurationTillNext() > 0 {
		logger.Info("Policy done, requeuing", "requeueAfter", loop.DurationTillNext())
		return ctrl.Result{RequeueAfter: loop.DurationTillNext()}, nil
	}

	return result, nil
}

func (r *policyLoop) DurationTillNext() time.Duration {
	if r.Policy.Status.Status != "Done" {
		return 0
	}
	if r.Policy.Status.LastRunTime.IsZero() {
		return 0
	}

	interval := r.Policy.Spec.Payload.Interval.Duration
	sleepFor := time.Until(r.Policy.Status.LastRunTime.Add(interval))
	return sleepFor
}

// SetupWithManager sets up the controller with the Manager.
func (r *WekaPolicyReconciler) SetupWithManager(mgr ctrl.Manager, wrappedReconcile reconcile.Reconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&weka.WekaPolicy{}).
		// Configuration policies decide one outcome between them, so one changing can change
		// whether the others are in effect. Without this, a policy that was Active before a
		// second one appeared keeps saying so while nothing is applied.
		Watches(&weka.WekaPolicy{}, handler.EnqueueRequestsFromMapFunc(r.siblingConfigurationPolicies)).
		WithOptions(controller.Options{MaxConcurrentReconciles: config.Config.MaxWorkers.WekaPolicy}).
		Complete(wrappedReconcile)
}

// siblingConfigurationPolicies maps a configuration policy event onto every other configuration
// policy in the same namespace, so their statuses are re-evaluated together.
func (r *WekaPolicyReconciler) siblingConfigurationPolicies(ctx context.Context, obj client.Object) []reconcile.Request {
	policy, ok := obj.(*weka.WekaPolicy)
	if !ok || policy.Spec.Payload.Configuration == nil {
		return nil
	}

	policyList := &weka.WekaPolicyList{}
	if err := r.List(ctx, policyList, &client.ListOptions{Namespace: policy.Namespace}); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for i := range policyList.Items {
		sibling := &policyList.Items[i]
		if sibling.Name == policy.Name || sibling.Spec.Payload.Configuration == nil {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: sibling.Name, Namespace: sibling.Namespace},
		})
	}
	return requests
}
