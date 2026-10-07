package wekacluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/weka/go-steps-engine/lifecycle"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

func TestPodRotationEnabled_OverrideWins(t *testing.T) {
	cases := []struct {
		name     string
		override *bool
		want     bool
	}{
		{"override true", ptr.To(true), true},
		{"override false", ptr.To(false), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cluster := &weka.WekaCluster{
				Spec: weka.WekaClusterSpec{
					Overrides: &weka.WekaClusterSpecOverrides{
						PodRotation: tc.override,
					},
				},
			}
			got := podRotationEnabled(context.Background(), cluster)
			if got != tc.want {
				t.Errorf("podRotationEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPodRotationEnabled_PolicyDefaultDecides(t *testing.T) {
	prevNs := config.Config.OperatorPodNamespace
	config.Config.OperatorPodNamespace = "weka-operator-system"
	t.Cleanup(func() {
		config.Config.OperatorPodNamespace = prevNs
	})
	ctx := context.Background()
	refresh := func(objs ...client.Object) {
		t.Helper()
		if err := services.RefreshSettings(ctx, newFakeClient(t, objs...)); err != nil {
			t.Fatalf("RefreshSettings: %v", err)
		}
	}
	t.Cleanup(func() { _ = services.RefreshSettings(ctx, newFakeClient(t)) })

	policy := &weka.WekaPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "configuration", Namespace: "weka-operator-system"},
		Spec: weka.WekaPolicySpec{Payload: weka.PolicyPayload{Configuration: &weka.ConfigurationPayload{
			PodRotation: &weka.PodRotationSpec{Enabled: ptr.To(true)},
		}}},
	}
	cluster := &weka.WekaCluster{}

	refresh(policy)
	if !podRotationEnabled(ctx, cluster) {
		t.Error("podRotationEnabled = false, want true from the policy default")
	}
	cluster.Spec.Overrides = &weka.WekaClusterSpecOverrides{PodRotation: ptr.To(false)}
	if podRotationEnabled(ctx, cluster) {
		t.Error("podRotationEnabled = true, want the cluster override to win over the policy")
	}
	cluster.Spec.Overrides = nil
	refresh()
	if podRotationEnabled(ctx, cluster) {
		t.Error("podRotationEnabled = true, want false without a policy")
	}
}

func rotationContainer(name, mode string, outdated, approved bool) *weka.WekaContainer {
	c := &weka.WekaContainer{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: "default",
		Labels: map[string]string{"weka.io/mode": mode},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "weka.weka.io/v1alpha1", Kind: "WekaCluster", Name: "cl1", UID: "cluster-uid",
		}},
	}}
	c.Spec.Mode = mode
	c.Spec.NodeAffinity = "node-1"
	c.Spec.RotatePod = approved
	c.Status.PodOutdated = outdated
	return c
}

func newRotationLoop(t *testing.T, rotation *bool, gate func() (bool, string), containers ...*weka.WekaContainer) *wekaClusterReconcilerLoop {
	t.Helper()
	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cl1", Namespace: "default", UID: "cluster-uid"}}
	cluster.Spec.Overrides = &weka.WekaClusterSpecOverrides{PodRotation: rotation}
	prev := clusterHealthGate
	clusterHealthGate = func(context.Context, *wekaClusterReconcilerLoop) (bool, string) {
		if gate == nil {
			return true, ""
		}
		return gate()
	}
	t.Cleanup(func() { clusterHealthGate = prev })
	return newUpgradeLoop(t, cluster, containers)
}

func getRotationContainer(t *testing.T, r *wekaClusterReconcilerLoop, name string) *weka.WekaContainer {
	t.Helper()
	got := &weka.WekaContainer{}
	if err := r.getClient().Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, got); err != nil {
		t.Fatalf("Get %s: %v", name, err)
	}
	return got
}

func rotatePodOf(t *testing.T, r *wekaClusterReconcilerLoop, name string) bool {
	t.Helper()
	return getRotationContainer(t, r, name).Spec.RotatePod
}

func requireWait(t *testing.T, err error, contains string) {
	t.Helper()
	var waitErr *lifecycle.WaitError
	if !errors.As(err, &waitErr) {
		t.Fatalf("error = %v, want WaitError", err)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Errorf("error = %q, want to contain %q", err, contains)
	}
}

func TestPodRotation_NothingOutdated(t *testing.T) {
	r := newRotationLoop(t, ptr.To(true), nil, rotationContainer("d1", weka.WekaContainerModeDrive, false, false))
	if err := r.handlePodRotation(context.Background()); err != nil {
		t.Fatalf("handlePodRotation: %v", err)
	}
	if rotatePodOf(t, r, "d1") {
		t.Error("d1 approved, want untouched")
	}
}

func TestPodRotation_DisabledIsEventOnly(t *testing.T) {
	r := newRotationLoop(t, ptr.To(false), nil, rotationContainer("d1", weka.WekaContainerModeDrive, true, false))
	if err := r.handlePodRotation(context.Background()); err != nil {
		t.Fatalf("handlePodRotation: %v", err)
	}
	if rotatePodOf(t, r, "d1") {
		t.Error("d1 approved with rotation disabled")
	}
}

func TestPodRotation_Paused(t *testing.T) {
	r := newRotationLoop(t, ptr.To(true), nil, rotationContainer("d1", weka.WekaContainerModeDrive, true, false))
	r.cluster.Spec.Overrides.UpgradePaused = true
	requireWait(t, r.handlePodRotation(context.Background()), "paused")
	if rotatePodOf(t, r, "d1") {
		t.Error("d1 approved while paused")
	}
}

func TestPodRotation_HealthGateBlocks(t *testing.T) {
	gate := func() (bool, string) { return false, "not fully protected" }
	r := newRotationLoop(t, ptr.To(true), gate, rotationContainer("d1", weka.WekaContainerModeDrive, true, false))
	requireWait(t, r.handlePodRotation(context.Background()), "not fully protected")
	if rotatePodOf(t, r, "d1") {
		t.Error("d1 approved while gate blocks")
	}
}

func TestPodRotation_ApprovesFirstInRoleOrder(t *testing.T) {
	r := newRotationLoop(t, ptr.To(true), nil,
		rotationContainer("c1", weka.WekaContainerModeCompute, true, false),
		rotationContainer("d2", weka.WekaContainerModeDrive, true, false))
	requireWait(t, r.handlePodRotation(context.Background()), "d2")
	if !rotatePodOf(t, r, "d2") || rotatePodOf(t, r, "c1") {
		t.Errorf("want only d2 approved, got d2=%v c1=%v", rotatePodOf(t, r, "d2"), rotatePodOf(t, r, "c1"))
	}
}

func TestPodRotation_SkipsDeletingAndNoNode(t *testing.T) {
	deleting := rotationContainer("d1", weka.WekaContainerModeDrive, true, false)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"test/finalizer"}
	noNode := rotationContainer("d2", weka.WekaContainerModeDrive, true, false)
	noNode.Spec.NodeAffinity = ""
	r := newRotationLoop(t, ptr.To(true), nil, deleting, noNode, rotationContainer("c1", weka.WekaContainerModeCompute, true, false))
	requireWait(t, r.handlePodRotation(context.Background()), "c1")
	if !rotatePodOf(t, r, "c1") || rotatePodOf(t, r, "d1") || rotatePodOf(t, r, "d2") {
		t.Error("want only c1 approved")
	}
}

func TestPodRotation_WaitsForInFlight(t *testing.T) {
	r := newRotationLoop(t, ptr.To(true), nil,
		rotationContainer("d1", weka.WekaContainerModeDrive, true, true),
		rotationContainer("d2", weka.WekaContainerModeDrive, true, false))
	requireWait(t, r.handlePodRotation(context.Background()), "in progress")
	if !rotatePodOf(t, r, "d1") || rotatePodOf(t, r, "d2") {
		t.Error("want d1 still approved and d2 not approved")
	}
}

func TestPodRotation_ClearsDone(t *testing.T) {
	r := newRotationLoop(t, ptr.To(true), nil,
		rotationContainer("d1", weka.WekaContainerModeDrive, false, true),
		rotationContainer("d2", weka.WekaContainerModeDrive, true, false))
	requireWait(t, r.handlePodRotation(context.Background()), "in progress")
	if rotatePodOf(t, r, "d1") {
		t.Error("d1 rotatePod not cleared")
	}
	if rotatePodOf(t, r, "d2") {
		t.Error("d2 approved in the same pass")
	}
}

func TestPodRotation_ClearsEvenWhenDisabled(t *testing.T) {
	r := newRotationLoop(t, ptr.To(false), nil, rotationContainer("d1", weka.WekaContainerModeDrive, false, true))
	requireWait(t, r.handlePodRotation(context.Background()), "in progress")
	if rotatePodOf(t, r, "d1") {
		t.Error("d1 rotatePod not cleared")
	}
}

func TestPodRotation_ClearsDeleted(t *testing.T) {
	c := rotationContainer("d1", weka.WekaContainerModeDrive, true, true)
	now := metav1.Now()
	c.DeletionTimestamp = &now
	c.Finalizers = []string{"test/finalizer"}
	r := newRotationLoop(t, ptr.To(true), nil, c)
	requireWait(t, r.handlePodRotation(context.Background()), "in progress")
	if rotatePodOf(t, r, "d1") {
		t.Error("deleted container rotatePod not cleared")
	}
}

func TestPodRotation_DeletingStateReleasedAndNeverApproved(t *testing.T) {
	for _, state := range []weka.ContainerState{weka.ContainerStateDeleting, weka.ContainerStateDestroying} {
		t.Run(string(state), func(t *testing.T) {
			stuck := rotationContainer("d1", weka.WekaContainerModeDrive, true, true)
			stuck.Spec.State = state
			never := rotationContainer("d2", weka.WekaContainerModeDrive, true, false)
			never.Spec.State = state
			r := newRotationLoop(t, ptr.To(true), nil, stuck, never, rotationContainer("c1", weka.WekaContainerModeCompute, true, false))
			requireWait(t, r.handlePodRotation(context.Background()), "in progress")
			if rotatePodOf(t, r, "d1") {
				t.Error("d1 rotatePod not cleared")
			}
			if rotatePodOf(t, r, "c1") {
				t.Error("c1 approved in the same pass")
			}
			requireWait(t, r.handlePodRotation(context.Background()), "c1")
			if !rotatePodOf(t, r, "c1") {
				t.Error("c1 not approved after the stuck approval was released")
			}
			if rotatePodOf(t, r, "d1") || rotatePodOf(t, r, "d2") {
				t.Error("container in deleting state approved")
			}
		})
	}
}

func TestPodRotation_AllAtOnceBatch(t *testing.T) {
	gate := func() (bool, string) { return false, "gate must be skipped" }
	r := newRotationLoop(t, ptr.To(true), gate,
		rotationContainer("d1", weka.WekaContainerModeDrive, true, false),
		rotationContainer("d2", weka.WekaContainerModeDrive, true, false),
		rotationContainer("c1", weka.WekaContainerModeCompute, true, false))
	r.cluster.Spec.Overrides.UpgradeAllAtOnce = true
	requireWait(t, r.handlePodRotation(context.Background()), "3 containers")
	for _, n := range []string{"d1", "d2", "c1"} {
		if !rotatePodOf(t, r, n) {
			t.Errorf("%s not approved", n)
		}
	}
}

func TestPodRotation_StaleCacheDoesNotDoubleApprove(t *testing.T) {
	r := newRotationLoop(t, ptr.To(true), nil,
		rotationContainer("d1", weka.WekaContainerModeDrive, true, false),
		rotationContainer("d2", weka.WekaContainerModeDrive, true, false))
	fresh := newFakeClient(t,
		rotationContainer("d1", weka.WekaContainerModeDrive, true, true),
		rotationContainer("d2", weka.WekaContainerModeDrive, true, false))
	r.Manager = fakeManagerWithClient{c: r.getClient(), reader: fresh}
	requireWait(t, r.handlePodRotation(context.Background()), "in progress")
	if rotatePodOf(t, r, "d2") {
		t.Error("d2 approved while d1 in flight")
	}
	if rotatePodOf(t, r, "d1") {
		t.Error("d1 in the cached client was patched, want the decision made on the uncached read only")
	}
}

func TestPodRotation_ImageChangeClearsFinishedThenRolls(t *testing.T) {
	r := newRotationLoop(t, ptr.To(true), nil, rotationContainer("d1", weka.WekaContainerModeDrive, false, true))
	r.cluster.Spec.Image = "new"
	r.cluster.Status.LastAppliedImage = "old"
	r.cluster.Spec.Overrides.UpgradeAllAtOnce = true
	r.containers[0].Spec.Image = "old"
	requireWait(t, r.handleUpgrade(context.Background()), "in-flight")
	if rotatePodOf(t, r, "d1") {
		t.Error("d1 rotatePod not cleared")
	}
	if got := getRotationContainer(t, r, "d1"); got.Spec.Image == "new" {
		t.Error("image patched in the same pass as the clear")
	}
	if err := r.handleUpgrade(context.Background()); err != nil {
		t.Fatalf("handleUpgrade: %v", err)
	}
	if got := getRotationContainer(t, r, "d1"); got.Spec.Image != "new" {
		t.Errorf("image = %q, want new once the finished rotation is cleared", got.Spec.Image)
	}
}

func TestPodRotation_ImageChangeWaitsForInFlight(t *testing.T) {
	r := newRotationLoop(t, ptr.To(true), nil, rotationContainer("d1", weka.WekaContainerModeDrive, true, true))
	r.cluster.Spec.Image = "new"
	r.cluster.Status.LastAppliedImage = "old"
	requireWait(t, r.handleUpgrade(context.Background()), "in-flight")
	if getRotationContainer(t, r, "d1").Spec.Image == "new" {
		t.Error("image patched while rotation in flight")
	}
}

func TestPodRotation_SsdproxyNeverApproved(t *testing.T) {
	p := rotationContainer("p1", weka.WekaContainerModeSSDProxy, true, false)
	p.OwnerReferences = nil
	r := newRotationLoop(t, ptr.To(true), nil, p)
	if err := r.handlePodRotation(context.Background()); err != nil {
		t.Fatalf("handlePodRotation: %v", err)
	}
	if rotatePodOf(t, r, "p1") {
		t.Error("ssdproxy approved")
	}
}

func TestPodRotation_OwnedNonBackendNeverApproved(t *testing.T) {
	for _, mode := range []string{weka.WekaContainerModeEnvoy, weka.WekaContainerModeTelemetry} {
		t.Run(mode, func(t *testing.T) {
			r := newRotationLoop(t, ptr.To(true), nil, rotationContainer("x1", mode, true, false))
			if err := r.handlePodRotation(context.Background()); err != nil {
				t.Fatalf("handlePodRotation: %v", err)
			}
			if rotatePodOf(t, r, "x1") {
				t.Errorf("%s container approved", mode)
			}
		})
	}
}
