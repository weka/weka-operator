package validation

import (
	"context"
	"errors"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/weka/weka-operator/internal/pkg/domain"
)

const modeFlipClusterUID = "cluster-uid-1"

// modeFlipCluster builds a WekaCluster with a stable UID, so the drive-container lookup can match.
func modeFlipCluster(dynamic *weka.WekaClusterTemplate) *weka.WekaCluster {
	c := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns", UID: types.UID(modeFlipClusterUID)},
	}
	c.Spec.Dynamic = dynamic
	return c
}

// modeFlipClient seeds a fake client with `n` drive containers belonging to modeFlipCluster.
func modeFlipClient(t *testing.T, driveContainers int) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(weka): %v", err)
	}
	b := fake.NewClientBuilder().WithScheme(scheme)
	for i := 0; i < driveContainers; i++ {
		wc := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "drive-" + string(rune('a'+i)),
				Namespace: "ns",
				Labels: map[string]string{
					domain.WekaLabelClusterId: modeFlipClusterUID,
					domain.WekaLabelMode:      weka.WekaContainerModeDrive,
				},
			},
		}
		b = b.WithObjects(wc)
	}
	return b.Build()
}

// modeFlipFailingClient returns a client whose every List fails, so a test can prove the validator
// either does or does not reach the API server.
func modeFlipFailingClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(weka): %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			return errors.New("etcdserver: request timed out")
		},
	}).Build()
}

// TestSizingModeFlip_LeavingTheMode is the case the policy exists for: adding container counts to a
// live daemonset cluster would start a second, differently sized drive-container population.
func TestSizingModeFlip_LeavingTheMode(t *testing.T) {
	v := &clusterSizingModeFlip{}
	ctx := context.Background()
	c := modeFlipClient(t, 3)

	old := modeFlipCluster(&weka.WekaClusterTemplate{})
	updated := modeFlipCluster(&weka.WekaClusterTemplate{ComputeContainers: 6, DriveContainers: 6})

	errs := v.ValidateUpdate(ctx, c, old, updated)
	if len(errs) != 1 {
		t.Fatalf("expected exactly one violation, got %v", errs)
	}
	detail := errs[0].Detail
	for _, want := range []string{
		"auto-full-drives (acts as a daemonset)",
		"explicit container counts",
		"drive containers already exist",
		"second, differently sized population",
		// The message must name the switches that ARE available, so the one-way rule is discoverable
		// from the rejection alone.
		"the only supported switches are",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("expected message to contain %q, got: %s", want, detail)
		}
	}
	// The user explicitly vetoed recreate-the-cluster as a remedy.
	for _, banned := range []string{"delete", "recreate", "re-create"} {
		if strings.Contains(strings.ToLower(detail), banned) {
			t.Errorf("message must not suggest deleting/recreating the cluster, got: %s", detail)
		}
	}
}

// TestSizingModeFlip_SupportedSwitches covers the two transitions the operator can carry over on a
// live cluster: counts -> daemonset (existing containers are adopted via Status.NodeAffinity and grown
// in place) and drive-sharing -> clusterCapacity (the documented in-place migration).
func TestSizingModeFlip_SupportedSwitches(t *testing.T) {
	v := &clusterSizingModeFlip{}
	ctx := context.Background()
	c := modeFlipClient(t, 3)

	cases := map[string][2]*weka.WekaClusterTemplate{
		"counts -> daemonset": {
			{ComputeContainers: 6, DriveContainers: 6},
			{},
		},
		"counts -> daemonset, template removed entirely": {
			{ComputeContainers: 6, DriveContainers: 6},
			nil,
		},
		"counts -> daemonset, keeping the numDrives pin": {
			{ComputeContainers: 6, DriveContainers: 6, NumDrives: 4},
			{NumDrives: 4},
		},
		"containerCapacity -> clusterCapacity": {
			{ContainerCapacity: 6000, DriveContainers: 6, ComputeContainers: 6},
			{ClusterCapacity: "500TiB"},
		},
		"numDrives+driveCapacity -> clusterCapacity": {
			{NumDrives: 6, DriveCapacity: 3500},
			{ClusterCapacity: "500TiB"},
		},
	}
	for name, pair := range cases {
		t.Run(name, func(t *testing.T) {
			errs := v.ValidateUpdate(ctx, c, modeFlipCluster(pair[0]), modeFlipCluster(pair[1]))
			if len(errs) != 0 {
				t.Errorf("expected a supported switch to be admitted, got %v", errs)
			}
		})
	}
}

// TestSizingModeFlip_SupportedSwitchNeverListsContainers: the allowlist is consulted before the
// drive-container lookup, so a switch we support is never hostage to apiserver health. Without this
// ordering, an etcd blip would reject the one mode change users are told to make.
func TestSizingModeFlip_SupportedSwitchNeverListsContainers(t *testing.T) {
	v := &clusterSizingModeFlip{}
	ctx := context.Background()
	c := modeFlipFailingClient(t)

	old := modeFlipCluster(&weka.WekaClusterTemplate{ComputeContainers: 6, DriveContainers: 6})
	updated := modeFlipCluster(&weka.WekaClusterTemplate{})

	if errs := v.ValidateUpdate(ctx, c, old, updated); len(errs) != 0 {
		t.Errorf("a supported switch must not touch the API server at all, got %v", errs)
	}
}

// TestSizingModeFlip_RejectedSwitches walks every transition the operator cannot carry over. The
// capacity pairs matter as much as the auto-full-drives ones: nothing else guards them — the
// cluster_capacity_* policies never see the old object, and chunk_feasibility disarms itself once
// drive containers exist.
func TestSizingModeFlip_RejectedSwitches(t *testing.T) {
	v := &clusterSizingModeFlip{}
	ctx := context.Background()
	c := modeFlipClient(t, 3)

	auto := &weka.WekaClusterTemplate{}
	counts := &weka.WekaClusterTemplate{ComputeContainers: 6, DriveContainers: 6}
	capacity := &weka.WekaClusterTemplate{ClusterCapacity: "500TiB"}
	sharing := &weka.WekaClusterTemplate{ContainerCapacity: 6000, DriveContainers: 6, ComputeContainers: 6}

	cases := map[string]struct {
		old, updated    *weka.WekaClusterTemplate
		wantConsequence string
	}{
		"daemonset -> counts":            {auto, counts, "second, differently sized population"},
		"daemonset -> clusterCapacity":   {auto, capacity, "second, differently sized population"},
		"daemonset -> drive-sharing":     {auto, sharing, "second, differently sized population"},
		"clusterCapacity -> daemonset":   {capacity, auto, "neither adopted nor grown"},
		"drive-sharing -> daemonset":     {sharing, auto, "neither adopted nor grown"},
		"counts -> clusterCapacity":      {counts, capacity, "wedging reconciliation"},
		"clusterCapacity -> counts":      {capacity, counts, "never auto-shrinks"},
		"clusterCapacity -> drive-shar.": {capacity, sharing, "never auto-shrinks"},
		"counts -> drive-sharing":        {counts, sharing, "nothing reconciles the two"},
		"drive-sharing -> counts":        {sharing, counts, "nothing reconciles the two"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			errs := v.ValidateUpdate(ctx, c, modeFlipCluster(tc.old), modeFlipCluster(tc.updated))
			if len(errs) != 1 {
				t.Fatalf("expected exactly one violation, got %v", errs)
			}
			detail := errs[0].Detail
			if !strings.Contains(detail, tc.wantConsequence) {
				t.Errorf("expected the %q consequence, got: %s", tc.wantConsequence, detail)
			}
			oldMode := derivedSizingMode(tc.old)
			newMode := derivedSizingMode(tc.updated)
			if !strings.Contains(detail, oldMode) || !strings.Contains(detail, newMode) {
				t.Errorf("expected message to name both modes (%s -> %s), got: %s", oldMode, newMode, detail)
			}
		})
	}
}

// TestSizingModeFlip_NilTemplateIsTheSameMode: unsetting the whole template from an already
// auto-full-drives cluster is not a flip — nil and {} are the same mode.
func TestSizingModeFlip_NilTemplateIsTheSameMode(t *testing.T) {
	v := &clusterSizingModeFlip{}
	ctx := context.Background()
	c := modeFlipClient(t, 3)

	if errs := v.ValidateUpdate(ctx, c, modeFlipCluster(&weka.WekaClusterTemplate{}), modeFlipCluster(nil)); len(errs) != 0 {
		t.Errorf("{} -> nil is the same mode, got %v", errs)
	}
	if errs := v.ValidateUpdate(ctx, c, modeFlipCluster(nil), modeFlipCluster(&weka.WekaClusterTemplate{NumDrives: 4})); len(errs) != 0 {
		t.Errorf("numDrives is a per-node override, not a mode change, got %v", errs)
	}
}

// TestSizingModeFlip_NoDriveContainersYet: before any drive container exists the mode is still free to
// change, which is what makes fixing a mistyped spec possible.
func TestSizingModeFlip_NoDriveContainersYet(t *testing.T) {
	v := &clusterSizingModeFlip{}
	ctx := context.Background()
	c := modeFlipClient(t, 0)

	old := modeFlipCluster(&weka.WekaClusterTemplate{})
	updated := modeFlipCluster(&weka.WekaClusterTemplate{ComputeContainers: 6, DriveContainers: 6})

	if errs := v.ValidateUpdate(ctx, c, old, updated); len(errs) != 0 {
		t.Errorf("expected no violation before any drive container exists, got %v", errs)
	}
}

// TestSizingModeFlip_ListFailureFailsClosed: if the drive containers cannot be listed, the mode change
// cannot be validated and must be REJECTED, not admitted. Swallowing the error into "no containers
// exist" would let an apiserver blip during a kubectl edit wave through the exact change this policy
// blocks — and unlike the blip, the resulting two-population topology persists.
func TestSizingModeFlip_ListFailureFailsClosed(t *testing.T) {
	v := &clusterSizingModeFlip{}
	ctx := context.Background()
	c := modeFlipFailingClient(t)

	old := modeFlipCluster(&weka.WekaClusterTemplate{})
	updated := modeFlipCluster(&weka.WekaClusterTemplate{ComputeContainers: 6, DriveContainers: 6})

	errs := v.ValidateUpdate(ctx, c, old, updated)
	if len(errs) != 1 {
		t.Fatalf("a List failure must reject, not admit; got %v", errs)
	}
	if errs[0].Type != field.ErrorTypeInternal {
		t.Errorf("expected an InternalError, got %v", errs[0].Type)
	}
	for _, want := range []string{
		"could not be validated and is rejected rather than risked",
		"etcdserver: request timed out",
		"Retry the edit",
	} {
		if !strings.Contains(errs[0].Detail, want) {
			t.Errorf("expected message to contain %q, got: %s", want, errs[0].Detail)
		}
	}
}

// TestSizingModeFlip_ListFailureIrrelevantWhenModeUnchanged: the container lookup happens only after
// the mode comparison, so an ordinary edit that does not flip the mode is never exposed to a List
// failure. Without this, every edit to a live cluster would be hostage to apiserver health.
func TestSizingModeFlip_ListFailureIrrelevantWhenModeUnchanged(t *testing.T) {
	v := &clusterSizingModeFlip{}
	ctx := context.Background()
	c := modeFlipFailingClient(t)

	old := modeFlipCluster(&weka.WekaClusterTemplate{})
	updated := modeFlipCluster(&weka.WekaClusterTemplate{NumDrives: 4}) // same mode, per-node override

	if errs := v.ValidateUpdate(ctx, c, old, updated); len(errs) != 0 {
		t.Errorf("a same-mode edit must not touch the API server at all, got %v", errs)
	}
}

// TestSizingModeFlip_SameModeIgnored: resizing within a mode is never this validator's business.
func TestSizingModeFlip_SameModeIgnored(t *testing.T) {
	v := &clusterSizingModeFlip{}
	ctx := context.Background()
	c := modeFlipClient(t, 3)

	cases := map[string][2]*weka.WekaClusterTemplate{
		"same mode, different counts": {
			{ComputeContainers: 6, DriveContainers: 6},
			{ComputeContainers: 8, DriveContainers: 8},
		},
		"same mode, different clusterCapacity": {
			{ClusterCapacity: "500TiB"},
			{ClusterCapacity: "800TiB"},
		},
	}
	for name, pair := range cases {
		t.Run(name, func(t *testing.T) {
			errs := v.ValidateUpdate(ctx, c, modeFlipCluster(pair[0]), modeFlipCluster(pair[1]))
			if len(errs) != 0 {
				t.Errorf("expected no violation, got %v", errs)
			}
		})
	}
}
