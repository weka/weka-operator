package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/weka/weka-operator/internal/capacityplanner"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

// feasibleLabels is used for BOTH role selectors, so one node counts toward both floors.
var feasibleLabels = map[string]string{"afd-feasible": "yes"}

func feasibleCluster(dynamic *weka.WekaClusterTemplate) *weka.WekaCluster {
	c := &weka.WekaCluster{}
	c.Spec.Dynamic = dynamic
	sel := feasibleLabels
	c.Spec.RoleNodeSelector.Drive = &sel
	c.Spec.RoleNodeSelector.Compute = &sel
	return c
}

// feasibleNode builds a labelled node with allocatable CPU/memory/hugepages the planner can size
// against. driveCapacitiesGiB == nil means sign-drives has not run (no annotation).
func feasibleNode(t *testing.T, name string, driveCapacitiesGiB []int) *corev1.Node {
	t.Helper()
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: feasibleLabels},
		Status: corev1.NodeStatus{
			// Ready, uncordoned and untainted: NodeIneligibleReason rejects anything else, and an
			// ineligible node contributes no drives — which would read as an unsigned fleet.
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("64"),
				corev1.ResourceMemory: resource.MustParse("512Gi"),
				corev1.ResourceName(string(corev1.ResourceHugePagesPrefix) + "2Mi"): resource.MustParse("256Gi"),
			},
		},
	}
	if driveCapacitiesGiB != nil {
		entries := make([]domain.DriveEntry, 0, len(driveCapacitiesGiB))
		for i, capGiB := range driveCapacitiesGiB {
			entries = append(entries, domain.DriveEntry{Serial: fmt.Sprintf("%s-d%d", name, i), CapacityGiB: capGiB})
		}
		b, err := json.Marshal(entries)
		if err != nil {
			t.Fatalf("marshal drive entries: %v", err)
		}
		n.Annotations = map[string]string{consts.AnnotationWekaFullDrives: string(b)}
	}
	return n
}

func feasibleClient(t *testing.T, nodes ...*corev1.Node) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(weka): %v", err)
	}
	// Mirrors the manager cache's metadata.ownerReferences.uid index (setupContainerIndexes in
	// cmd/manager/main.go); the fake client rejects that field selector without it.
	b := fake.NewClientBuilder().WithScheme(scheme).
		WithIndex(&weka.WekaContainer{}, "metadata.ownerReferences.uid", func(rawObj client.Object) []string {
			owner := metav1.GetControllerOf(rawObj)
			if owner == nil {
				return nil
			}
			return []string{string(owner.UID)}
		})
	for _, n := range nodes {
		b = b.WithObjects(n)
	}
	return b.Build()
}

// TestAutoFullDrivesFeasible_NotAutoFullDrivesMode: a pinned count takes the template out of this mode,
// so the rule must not run the planner at all.
func TestAutoFullDrivesFeasible_NotAutoFullDrivesMode(t *testing.T) {
	withFormClusterMinContainers(t, 1, 1)
	v := &clusterAutoFullDrivesFeasible{}

	cluster := feasibleCluster(&weka.WekaClusterTemplate{DriveContainers: 5, ComputeContainers: 5})
	// A client that errors on any List proves no API read happens once the mode gate rejects.
	c := feasibleFailingClient(t)

	if errs := v.Validate(context.Background(), c, cluster); len(errs) != 0 {
		t.Fatalf("expected no errors outside auto-full-drives mode, got %v", errs)
	}
}

// TestAutoFullDrivesFeasible_BelowFloorDefersToMinNodes: too few matched nodes is min_nodes' case, and
// reporting the planner's compute-layout failure as well would describe one misconfiguration twice.
func TestAutoFullDrivesFeasible_BelowFloorDefersToMinNodes(t *testing.T) {
	withFormClusterMinContainers(t, 5, 5)
	v := &clusterAutoFullDrivesFeasible{}

	c := feasibleClient(t, feasibleNode(t, "n0", []int{1000}), feasibleNode(t, "n1", []int{1000}))

	if errs := v.Validate(context.Background(), c, feasibleCluster(&weka.WekaClusterTemplate{})); len(errs) != 0 {
		t.Fatalf("expected deferral to cluster_auto_full_drives_min_nodes, got %v", errs)
	}
}

// TestAutoFullDrivesFeasible_BelowFloorStillReportsDrivePins: the deferral above covers only the compute leg.
func TestAutoFullDrivesFeasible_BelowFloorStillReportsDrivePins(t *testing.T) {
	withFormClusterMinContainers(t, 5, 5)
	v := &clusterAutoFullDrivesFeasible{}

	// Two nodes against a floor of five, pinned at more drives than any node has: below floor AND impossible.
	c := feasibleClient(t, feasibleNode(t, "n0", []int{1000, 1000}), feasibleNode(t, "n1", []int{1000, 1000}))
	cluster := feasibleCluster(&weka.WekaClusterTemplate{NumDrives: 4})

	errs := v.Validate(context.Background(), c, cluster)
	if len(errs) != 1 {
		t.Fatalf("expected the pin to be reported below the floor, got %d: %v", len(errs), errs)
	}
	if got, want := errs[0].Field, "spec.dynamicTemplate.numDrives"; got != want {
		t.Errorf("field = %q, want %q", got, want)
	}
}

// TestAutoFullDrivesFeasible_UnsignedFleetIsBootstrap: labelling and drive-signing are independent, so a
// labelled but unsigned fleet is a normal pre-signing state, not a bad spec.
func TestAutoFullDrivesFeasible_UnsignedFleetIsBootstrap(t *testing.T) {
	withFormClusterMinContainers(t, 1, 1)
	v := &clusterAutoFullDrivesFeasible{}

	c := feasibleClient(t, feasibleNode(t, "n0", nil), feasibleNode(t, "n1", nil))

	if errs := v.Validate(context.Background(), c, feasibleCluster(&weka.WekaClusterTemplate{})); len(errs) != 0 {
		t.Fatalf("expected no errors before drives are signed, got %v", errs)
	}
}

// TestAutoFullDrivesFeasible_NumDrivesPinAboveSignedDrives: the planner's own verdict, surfaced at
// admission and attributed to the pin that caused it.
func TestAutoFullDrivesFeasible_NumDrivesPinAboveSignedDrives(t *testing.T) {
	withFormClusterMinContainers(t, 1, 1)
	v := &clusterAutoFullDrivesFeasible{}

	// Two signed drives per node, pinned at 4: no node can supply the pin.
	c := feasibleClient(t, feasibleNode(t, "n0", []int{1000, 1000}), feasibleNode(t, "n1", []int{1000, 1000}))
	cluster := feasibleCluster(&weka.WekaClusterTemplate{NumDrives: 4})

	errs := v.Validate(context.Background(), c, cluster)
	if len(errs) != 1 {
		t.Fatalf("expected exactly one error, got %d: %v", len(errs), errs)
	}
	if got, want := errs[0].Field, "spec.dynamicTemplate.numDrives"; got != want {
		t.Errorf("field = %q, want %q", got, want)
	}
	// The message must be the planner's, carrying its remedies — not a re-worded projection.
	if !strings.Contains(errs[0].Detail, "numDrives") || !strings.Contains(errs[0].Detail, "signed full drive") {
		t.Errorf("detail does not read as the planner's verdict: %q", errs[0].Detail)
	}
	if !strings.Contains(errs[0].Detail, " — ") {
		t.Errorf("detail carries no fixes from the planner: %q", errs[0].Detail)
	}
}

// TestAutoFullDrivesFeasible_FeasibleFleetPasses guards against the rule rejecting a plan the planner
// accepts — the failure mode that would block every daemonset cluster.
func TestAutoFullDrivesFeasible_FeasibleFleetPasses(t *testing.T) {
	withFormClusterMinContainers(t, 1, 1)
	v := &clusterAutoFullDrivesFeasible{}

	var nodes []*corev1.Node
	for i := 0; i < 5; i++ {
		nodes = append(nodes, feasibleNode(t, fmt.Sprintf("n%d", i), []int{1000, 1000}))
	}
	c := feasibleClient(t, nodes...)

	if errs := v.Validate(context.Background(), c, feasibleCluster(&weka.WekaClusterTemplate{})); len(errs) != 0 {
		t.Fatalf("expected a feasible fleet to pass, got %v", errs)
	}
}

// TestAutoFullDrivesFeasible_ListFailureFailsClosed: treating a List failure as "floors are met" would
// hand the case to the planner and report it in the wrong voice.
func TestAutoFullDrivesFeasible_ListFailureFailsClosed(t *testing.T) {
	withFormClusterMinContainers(t, 1, 1)
	v := &clusterAutoFullDrivesFeasible{}

	errs := v.Validate(context.Background(), feasibleFailingClient(t), feasibleCluster(&weka.WekaClusterTemplate{}))
	if len(errs) != 1 {
		t.Fatalf("expected one error on a List failure, got %d: %v", len(errs), errs)
	}
	if errs[0].Type != field.ErrorTypeInternal {
		t.Errorf("type = %v, want %v", errs[0].Type, field.ErrorTypeInternal)
	}
}

func feasibleFailingClient(t *testing.T) client.Client {
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

// TestInfeasibilityField: the planner names the pinned field it blamed, and a rejection caused by the
// fleet rather than the spec must blame nothing — a report carrying a resource-dimension Binding with no
// pin behind it (the compute leg's internal-inconsistency report, or any drive-container fit failure) once
// produced "Invalid value: 0" against a field the operator never set.
func TestInfeasibilityField(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report capacityplanner.InfeasibilityReport
		want   string
	}{
		{"numDrives pin", capacityplanner.InfeasibilityReport{Pool: "drive", Binding: "numDrives", SpecField: "numDrives"}, "spec.dynamicTemplate.numDrives"},
		{"driveCores pin", capacityplanner.InfeasibilityReport{Pool: "drive", Binding: "driveCores", SpecField: "driveCores"}, "spec.dynamicTemplate.driveCores"},
		{"computeCores pin", capacityplanner.InfeasibilityReport{Pool: "compute", Binding: "cores", SpecField: "computeCores"}, "spec.dynamicTemplate.computeCores"},

		{"internal inconsistency, no pin", capacityplanner.InfeasibilityReport{Pool: "compute", Binding: "driveCores"}, "spec.dynamicTemplate"},
		{"drive fit failure", capacityplanner.InfeasibilityReport{Pool: "drive", Binding: "memory"}, "spec.dynamicTemplate"},
		{"derived compute layout", capacityplanner.InfeasibilityReport{Pool: "compute", Binding: "hugepages"}, "spec.dynamicTemplate"},
	} {
		dyn := &weka.WekaClusterTemplate{NumDrives: 4, DriveCores: 3, ComputeCores: 2}
		got, value := infeasibilityField(dyn, &tc.report)
		if got.String() != tc.want {
			t.Errorf("%s: path = %q, want %q", tc.name, got, tc.want)
		}
		// A named field must carry its current value, and an unattributable one must render no value at
		// all: a nil BadValue reaches the operator as "Invalid value: null".
		if msg := field.Invalid(got, value, "detail").Error(); strings.Contains(msg, "null") {
			t.Errorf("%s: %q renders a null value", tc.name, msg)
		}
		if tc.report.SpecField == "" {
			if value != (field.OmitValueType{}) {
				t.Errorf("%s: value = %v, want the omit sentinel when nothing is attributable", tc.name, value)
			}
		} else if value == nil || value == (field.OmitValueType{}) {
			t.Errorf("%s: value = %v, want the pinned value for %s", tc.name, value, tc.report.SpecField)
		}
	}
}

// TestAutoFullDrivesFeasible_ExistingContainersGoThroughFieldIndex is the only test that reaches
// discovery.GetClusterContainers: every other case has no UID and takes the greenfield early return.
// It pins the field-index contract, which fails LOUDLY rather than emptily — a MatchingFields list against
// a client with no such index errors, and this rule fails closed on that, so a drifted index name would
// reject every auto-full-drives cluster on UPDATE. It is also the only coverage of the grow path, where
// ExistingDrives/ExistingCompute see a non-empty slice.
func TestAutoFullDrivesFeasible_ExistingContainersGoThroughFieldIndex(t *testing.T) {
	withFormClusterMinContainers(t, 1, 1)
	v := &clusterAutoFullDrivesFeasible{}

	cluster := feasibleCluster(&weka.WekaClusterTemplate{})
	cluster.Name, cluster.Namespace, cluster.UID = "afd", "default", "afd-uid-1"

	var nodes []*corev1.Node
	for i := 0; i < 3; i++ {
		nodes = append(nodes, feasibleNode(t, fmt.Sprintf("n%d", i), []int{1000, 1000}))
	}
	// One drive container this cluster already owns on n0, so the planner grows rather than creates.
	isController := true
	own := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name: "afd-drive-0", Namespace: "default",
			Labels: map[string]string{"weka.io/cluster-id": "afd-uid-1", "weka.io/mode": weka.WekaContainerModeDrive},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: weka.GroupVersion.String(), Kind: "WekaCluster",
				Name: "afd", UID: cluster.UID, Controller: &isController,
			}},
		},
		Spec: weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive, NumCores: 2, NumDrives: 2},
	}

	errs := v.Validate(context.Background(), feasibleClientWithOwned(t, own, nodes...), cluster)
	if len(errs) != 0 {
		t.Fatalf("expected a feasible grow plan to pass, got %v", errs)
	}

	// And the lookup itself really returned the container, rather than silently yielding nothing.
	got, err := clusterOwnContainers(context.Background(), feasibleClientWithOwned(t, own, nodes...), cluster)
	if err != nil {
		t.Fatalf("clusterOwnContainers: %v", err)
	}
	if len(got) != 1 || got[0].Name != "afd-drive-0" {
		t.Fatalf("own containers = %v, want just afd-drive-0 — the field index did not match", got)
	}
}

func feasibleClientWithOwned(t *testing.T, own *weka.WekaContainer, nodes ...*corev1.Node) client.Client {
	t.Helper()
	c := feasibleClient(t, nodes...)
	if err := c.Create(context.Background(), own.DeepCopy()); err != nil {
		t.Fatalf("seeding the owned container: %v", err)
	}
	return c
}

// TestClusterOwnContainers_NoUIDOnCreate: on CREATE there is no UID, so there is nothing to list and
// the planner must see a greenfield fleet rather than an error.
func TestClusterOwnContainers_NoUIDOnCreate(t *testing.T) {
	own, err := clusterOwnContainers(context.Background(), feasibleFailingClient(t), &weka.WekaCluster{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if own != nil {
		t.Errorf("own = %v, want nil", own)
	}
}
