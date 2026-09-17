package controllers

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/services"
)

func configurationReconciler(t *testing.T, policy *weka.WekaPolicy) *WekaPolicyReconciler {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("add weka scheme: %v", err)
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(policy).
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

// countingCache records Invalidate calls so a test can prove the reconciler drops the cached copy.
type countingCache struct {
	services.ConfigurationCacheService
	invalidations int
}

func (c *countingCache) Invalidate() { c.invalidations++ }

// The reconciler knows the moment the policy changes, so it must drop the cache rather than leave
// every edit waiting out the TTL. It has to do so even when the status is already Active, or an
// edit to a settled policy would never invalidate.
func TestReconcileConfigurationInvalidatesCache(t *testing.T) {
	policy := newConfigurationPolicy()
	r := configurationReconciler(t, policy)
	ctx := context.Background()

	previous := services.ConfigurationCache
	counter := &countingCache{ConfigurationCacheService: previous}
	services.ConfigurationCache = counter
	t.Cleanup(func() { services.ConfigurationCache = previous })

	if _, err := r.reconcileConfiguration(ctx, policy); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if counter.invalidations != 1 {
		t.Fatalf("invalidations = %d after first reconcile, want 1", counter.invalidations)
	}

	// already Active, so this hits the early return - and must still invalidate
	if _, err := r.reconcileConfiguration(ctx, policy); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if counter.invalidations != 2 {
		t.Errorf("invalidations = %d after the early-return pass, want 2", counter.invalidations)
	}
}

// A policy the type resolver rejects must say so on the object. Returning the error alone leaves
// kubectl showing a blank status while the reconciler retries forever.
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
