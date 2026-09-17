package controllers

import (
	"context"
	"strings"
	"testing"
	"time"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services"
)

func configurationReconciler(t *testing.T, policies ...*weka.WekaPolicy) *WekaPolicyReconciler {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("add weka scheme: %v", err)
	}

	objects := make([]client.Object, 0, len(policies))
	for _, p := range policies {
		objects = append(objects, p)
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&weka.WekaPolicy{}).
		Build()

	return &WekaPolicyReconciler{Client: c, Scheme: scheme}
}

func newConfigurationPolicy() *weka.WekaPolicy {
	return &weka.WekaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "operator-configuration", Namespace: "weka-operator-system"},
		Spec: weka.WekaPolicySpec{
			Payload: weka.PolicyPayload{
				Configuration: &weka.ConfigurationPayload{},
			},
		},
	}
}

// The CRD marks status.lastRunTime required, and a zero metav1.Time marshals to null. Leaving it
// unset makes the API server reject the status update, which returns an error from Reconcile and
// puts the policy in a permanent retry loop.
func TestReconcileConfigurationSetsRequiredStatusFields(t *testing.T) {
	previous := config.Config.OperatorPodNamespace
	config.Config.OperatorPodNamespace = "weka-operator-system"
	t.Cleanup(func() { config.Config.OperatorPodNamespace = previous })

	policy := newConfigurationPolicy()
	r := configurationReconciler(t, policy)
	ctx := context.Background()

	if _, err := r.reconcileConfiguration(ctx, policy); err != nil {
		t.Fatalf("reconcileConfiguration: %v", err)
	}

	stored := &weka.WekaPolicy{}
	key := types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}
	if err := r.Get(ctx, key, stored); err != nil {
		t.Fatalf("get policy: %v", err)
	}

	if stored.Status.Status != configurationPolicyStatus {
		t.Errorf("status = %q, want %q", stored.Status.Status, configurationPolicyStatus)
	}
	if stored.Status.LastRunTime.IsZero() {
		t.Error("lastRunTime is zero; it is required by the CRD and null is rejected by the API server")
	}
}

// A second pass must not write again: an unconditional status update trips the controller's own
// watch and spins.
func TestReconcileConfigurationIsIdempotent(t *testing.T) {
	policy := newConfigurationPolicy()
	r := configurationReconciler(t, policy)
	ctx := context.Background()

	if _, err := r.reconcileConfiguration(ctx, policy); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	stored := &weka.WekaPolicy{}
	key := types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}
	if err := r.Get(ctx, key, stored); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	firstVersion := stored.ResourceVersion

	if _, err := r.reconcileConfiguration(ctx, stored); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	again := &weka.WekaPolicy{}
	if err := r.Get(ctx, key, again); err != nil {
		t.Fatalf("get policy again: %v", err)
	}
	if again.ResourceVersion != firstVersion {
		t.Errorf("resourceVersion changed from %s to %s; the second pass wrote status again",
			firstVersion, again.ResourceVersion)
	}
}

// The reconciler must re-resolve on every pass, including one that changes no status, so an edit
// to an already-Active policy takes effect at once instead of waiting for the background refresh.
func TestReconcileConfigurationRefreshesSettings(t *testing.T) {
	previous := config.Config.OperatorPodNamespace
	config.Config.OperatorPodNamespace = "weka-operator-system"
	t.Cleanup(func() { config.Config.OperatorPodNamespace = previous })

	policy := newConfigurationPolicy()
	force := true
	policy.Spec.Payload.Configuration.Drivers = &weka.DriversSpec{ForceBuilderCli: &force}

	r := configurationReconciler(t, policy)
	ctx := context.Background()

	if _, err := r.reconcileConfiguration(ctx, policy); err != nil {
		t.Fatalf("reconcileConfiguration: %v", err)
	}
	if !services.GetSettings(ctx).Drivers.ForceBuilderCli {
		t.Error("the reconciler did not make the policy value readable through GetSettings")
	}

	// second pass returns early on unchanged status, and must still re-resolve
	force = false
	policy.Spec.Payload.Configuration.Drivers = &weka.DriversSpec{ForceBuilderCli: &force}
	if err := r.Update(ctx, policy); err != nil {
		t.Fatalf("update policy: %v", err)
	}
	if _, err := r.reconcileConfiguration(ctx, policy); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if services.GetSettings(ctx).Drivers.ForceBuilderCli {
		t.Error("an edit to an Active policy was not picked up on the early-return pass")
	}
}

func TestReconcileReportsTypeResolutionFailure(t *testing.T) {
	// configuration alongside a runnable payload: GetType refuses to pick between them
	policy := newConfigurationPolicy()
	policy.Spec.Payload.SignDrives = &weka.SignDrivesPayload{}

	r := configurationReconciler(t, policy)
	ctx := context.Background()

	_, _, typeErr := policy.GetType()
	if typeErr == nil {
		t.Fatal("expected GetType to reject configuration alongside signDrivesPayload")
	}

	r.reportPolicyFailure(ctx, policy, typeErr)

	stored := &weka.WekaPolicy{}
	key := types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}
	if err := r.Get(ctx, key, stored); err != nil {
		t.Fatalf("get policy: %v", err)
	}

	if stored.Status.Status != policyFailedStatus {
		t.Errorf("status = %q, want %q", stored.Status.Status, policyFailedStatus)
	}
	if stored.Status.LastResult != typeErr.Error() {
		t.Errorf("lastResult = %q, want the resolver error", stored.Status.LastResult)
	}
	if stored.Status.LastRunTime.IsZero() {
		t.Error("lastRunTime is zero; the CRD requires it and rejects null")
	}

	// a repeat pass with the same cause must not rewrite status and re-trigger the watch
	before := stored.ResourceVersion
	r.reportPolicyFailure(ctx, stored, typeErr)
	again := &weka.WekaPolicy{}
	if err := r.Get(ctx, key, again); err != nil {
		t.Fatalf("get policy again: %v", err)
	}
	if again.ResourceVersion != before {
		t.Errorf("resourceVersion changed from %s to %s; status was rewritten unchanged", before, again.ResourceVersion)
	}
}

// A configuration policy outside the operator namespace is never read, so reporting Active would
// tell the user a setting is in effect when it is silently ignored.
func TestReconcileConfigurationIgnoresForeignNamespace(t *testing.T) {
	previous := config.Config.OperatorPodNamespace
	config.Config.OperatorPodNamespace = "weka-operator-system"
	t.Cleanup(func() { config.Config.OperatorPodNamespace = previous })

	policy := newConfigurationPolicy()
	policy.Namespace = "tenant-a"

	r := configurationReconciler(t, policy)
	ctx := context.Background()

	if _, err := r.reconcileConfiguration(ctx, policy); err != nil {
		t.Fatalf("reconcileConfiguration: %v", err)
	}

	stored := &weka.WekaPolicy{}
	key := types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}
	if err := r.Get(ctx, key, stored); err != nil {
		t.Fatalf("get policy: %v", err)
	}

	if stored.Status.Status != configurationPolicyIgnoredStatus {
		t.Errorf("status = %q, want %q", stored.Status.Status, configurationPolicyIgnoredStatus)
	}
	if !strings.Contains(stored.Status.LastResult, "weka-operator-system") {
		t.Errorf("lastResult = %q, want it to name the operator namespace", stored.Status.LastResult)
	}
	if stored.Status.LastRunTime.IsZero() {
		t.Error("lastRunTime is zero; the CRD requires it and rejects null")
	}

	// repeat passes must not rewrite status and re-trigger the watch
	before := stored.ResourceVersion
	if _, err := r.reconcileConfiguration(ctx, stored); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	again := &weka.WekaPolicy{}
	if err := r.Get(ctx, key, again); err != nil {
		t.Fatalf("get policy again: %v", err)
	}
	if again.ResourceVersion != before {
		t.Errorf("resourceVersion changed from %s to %s; status was rewritten unchanged", before, again.ResourceVersion)
	}
}

// The policy in the operator namespace keeps working as before.
func TestReconcileConfigurationAcceptsOperatorNamespace(t *testing.T) {
	previous := config.Config.OperatorPodNamespace
	config.Config.OperatorPodNamespace = "weka-operator-system"
	t.Cleanup(func() { config.Config.OperatorPodNamespace = previous })

	policy := newConfigurationPolicy()
	r := configurationReconciler(t, policy)

	if _, err := r.reconcileConfiguration(context.Background(), policy); err != nil {
		t.Fatalf("reconcileConfiguration: %v", err)
	}
	if policy.Status.Status != configurationPolicyStatus {
		t.Errorf("status = %q, want %q", policy.Status.Status, configurationPolicyStatus)
	}
}

// A policy that was rejected and then fixed must not keep advertising the old reason: "Active"
// next to a failure message reads as a contradiction.
func TestReconcileConfigurationClearsStaleResultOnRecovery(t *testing.T) {
	previous := config.Config.OperatorPodNamespace
	config.Config.OperatorPodNamespace = "weka-operator-system"
	t.Cleanup(func() { config.Config.OperatorPodNamespace = previous })

	policy := newConfigurationPolicy()
	policy.Status.Status = policyFailedStatus
	policy.Status.LastResult = "configurationPayload cannot be combined with spec.type \"sign-drives\""
	policy.Status.LastRunTime = metav1.NewTime(time.Now().Add(-time.Hour))

	r := configurationReconciler(t, policy)
	ctx := context.Background()

	if _, err := r.reconcileConfiguration(ctx, policy); err != nil {
		t.Fatalf("reconcileConfiguration: %v", err)
	}

	stored := &weka.WekaPolicy{}
	key := types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}
	if err := r.Get(ctx, key, stored); err != nil {
		t.Fatalf("get policy: %v", err)
	}

	if stored.Status.Status != configurationPolicyStatus {
		t.Errorf("status = %q, want %q", stored.Status.Status, configurationPolicyStatus)
	}
	if stored.Status.LastResult != "" {
		t.Errorf("lastResult = %q, want it cleared once the policy is usable again", stored.Status.LastResult)
	}

	// and it must still be idempotent now that lastResult participates in the comparison
	before := stored.ResourceVersion
	if _, err := r.reconcileConfiguration(ctx, stored); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	again := &weka.WekaPolicy{}
	if err := r.Get(ctx, key, again); err != nil {
		t.Fatalf("get policy again: %v", err)
	}
	if again.ResourceVersion != before {
		t.Errorf("resourceVersion changed from %s to %s; status was rewritten unchanged", before, again.ResourceVersion)
	}
}

// Two ambiguous policies must not both report being in effect while neither is.
func TestReconcileConfigurationReportsUnresolvableConfiguration(t *testing.T) {
	previous := config.Config.OperatorPodNamespace
	config.Config.OperatorPodNamespace = "weka-operator-system"
	t.Cleanup(func() { config.Config.OperatorPodNamespace = previous })

	first := newConfigurationPolicy()
	second := newConfigurationPolicy()
	second.Name = "operator-configuration-2"

	r := configurationReconciler(t, first, second)
	ctx := context.Background()

	if _, err := r.reconcileConfiguration(ctx, first); err != nil {
		t.Fatalf("reconcileConfiguration: %v", err)
	}

	stored := &weka.WekaPolicy{}
	key := types.NamespacedName{Name: first.Name, Namespace: first.Namespace}
	if err := r.Get(ctx, key, stored); err != nil {
		t.Fatalf("get policy: %v", err)
	}

	if stored.Status.Status != policyFailedStatus {
		t.Errorf("status = %q, want %q: the configuration is unusable, so the policy is not in effect",
			stored.Status.Status, policyFailedStatus)
	}
	if !strings.Contains(stored.Status.LastResult, "configuration WekaPolicies") {
		t.Errorf("lastResult = %q, want it to explain the ambiguity", stored.Status.LastResult)
	}
}

// Configuration policies decide one outcome between them, so a change to any of them has to
// re-evaluate the rest: otherwise a policy that was Active before a second one appeared keeps
// saying so while nothing is in effect.
func TestSiblingConfigurationPoliciesEnqueuesTheOthers(t *testing.T) {
	first := newConfigurationPolicy()
	second := newConfigurationPolicy()
	second.Name = "operator-configuration-2"

	unrelated := &weka.WekaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "sign-drives", Namespace: first.Namespace},
		Spec:       weka.WekaPolicySpec{Payload: weka.PolicyPayload{SignDrives: &weka.SignDrivesPayload{}}},
	}

	r := configurationReconciler(t, first, second, unrelated)
	ctx := context.Background()

	requests := r.siblingConfigurationPolicies(ctx, first)
	if len(requests) != 1 {
		t.Fatalf("got %d requests, want 1 (the other configuration policy)", len(requests))
	}
	if requests[0].Name != second.Name {
		t.Errorf("enqueued %q, want %q", requests[0].Name, second.Name)
	}

	t.Run("a non-configuration policy enqueues nothing", func(t *testing.T) {
		if got := r.siblingConfigurationPolicies(ctx, unrelated); len(got) != 0 {
			t.Errorf("got %d requests for a sign-drives policy, want 0", len(got))
		}
	})

	t.Run("a lone configuration policy enqueues nothing", func(t *testing.T) {
		solo := configurationReconciler(t, first)
		if got := solo.siblingConfigurationPolicies(ctx, first); len(got) != 0 {
			t.Errorf("got %d requests with no siblings, want 0", len(got))
		}
	})
}
