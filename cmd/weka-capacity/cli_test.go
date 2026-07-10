package main

import (
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/capacityplanner"
	"github.com/weka/weka-operator/internal/capacityplanner/inventory"
	"github.com/weka/weka-operator/internal/controllers/allocator"
)

func intPtr(i int) *int           { return &i }
func floatPtr(f float64) *float64 { return &f }
func boolPtr(b bool) *bool        { return &b }

func TestParseSelector(t *testing.T) {
	cases := []struct {
		in      string
		want    map[string]string
		wantErr bool
	}{
		{"", map[string]string{}, false},
		{"weka.io/supports-backends=true", map[string]string{"weka.io/supports-backends": "true"}, false},
		{"a=1,b=2", map[string]string{"a": "1", "b": "2"}, false},
		{" a = 1 ", map[string]string{"a ": " 1"}, false}, // only the pair is trimmed, not the k/v
		{"bad", nil, true},
		{"=v", nil, true},
	}
	for _, tc := range cases {
		got, err := parseSelector(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseSelector(%q) expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSelector(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("parseSelector(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for k, v := range tc.want {
			if got[k] != v {
				t.Errorf("parseSelector(%q)[%q] = %q, want %q", tc.in, k, got[k], v)
			}
		}
	}
}

func TestParseRatio(t *testing.T) {
	r, err := parseRatio("1:90")
	if err != nil || r.Tlc != 1 || r.Qlc != 90 {
		t.Fatalf("parseRatio(1:90) = %+v, err=%v; want {1,90}", r, err)
	}
	if _, err := parseRatio("1"); err == nil {
		t.Errorf("parseRatio(1) expected error")
	}
	if _, err := parseRatio("a:b"); err == nil {
		t.Errorf("parseRatio(a:b) expected error")
	}
}

func TestRatioString(t *testing.T) {
	if got := ratioString(nil); !strings.Contains(got, "TLC-only") {
		t.Errorf("ratioString(nil) = %q, want a TLC-only default note", got)
	}
	if got := ratioString(&weka.DriveTypesRatio{Tlc: 3, Qlc: 7}); got != "3:7" {
		t.Errorf("ratioString({3,7}) = %q, want 3:7", got)
	}
}

// TestApplyConstraintOverrides verifies the top-layer precedence: only set (non-nil) flags override the
// base, and unset flags leave the base value untouched.
func TestApplyConstraintOverrides(t *testing.T) {
	base := &allocator.CapacityConstraints{
		TlcCapacityPerCoreGiB:    5120,
		QlcCapacityPerCoreGiB:    51200,
		ImbalanceFactor:          8.0,
		CapacityDeadbandFraction: 0.05,
		MinGrowthFraction:        0.2,
		AllowInPlaceGrowth:       false,
		AllowSingleParity:        false,
	}
	applyConstraintOverrides(base, &constraintFlags{
		TlcPerCoreGiB:        intPtr(1024),
		MinGrowthFraction:    floatPtr(0.5),
		EnableDynamicScaling: boolPtr(true),
		AllowSingleParity:    boolPtr(true),
	})
	if base.TlcCapacityPerCoreGiB != 1024 {
		t.Errorf("TlcCapacityPerCoreGiB = %d, want 1024 (overridden)", base.TlcCapacityPerCoreGiB)
	}
	if base.MinGrowthFraction != 0.5 {
		t.Errorf("MinGrowthFraction = %v, want 0.5 (overridden)", base.MinGrowthFraction)
	}
	if !base.AllowInPlaceGrowth || !base.AllowSingleParity {
		t.Errorf("bool overrides not applied: inPlace=%v singleParity=%v", base.AllowInPlaceGrowth, base.AllowSingleParity)
	}
	// Untouched by unset flags.
	if base.QlcCapacityPerCoreGiB != 51200 || base.ImbalanceFactor != 8.0 || base.CapacityDeadbandFraction != 0.05 {
		t.Errorf("unset flags must not change base: qlc=%d imb=%v deadband=%v",
			base.QlcCapacityPerCoreGiB, base.ImbalanceFactor, base.CapacityDeadbandFraction)
	}
}

// TestComputeGrowDiff covers the compute create/grow derivation from the ComputeLayout vs existing.
func TestComputeGrowDiff(t *testing.T) {
	existing := []capacityplanner.ExistingComputeContainer{
		{Name: "c-n1", Node: "n1", NumCores: 4},
		{Name: "c-n2", Node: "n2", NumCores: 8},
	}
	layout := []capacityplanner.ComputeContainerSpec{
		{Node: "n1", NumCores: 8, HugepagesMiB: 12800}, // grow 4->8
		{Node: "n2", NumCores: 8, HugepagesMiB: 12800}, // unchanged (no row)
		{Node: "n3", NumCores: 6, HugepagesMiB: 9600},  // create
	}
	create, grow := computeGrowDiff(existing, layout)
	if len(create) != 1 || create[0].Node != "n3" || create[0].Name != "" || create[0].ToCores != 6 || create[0].Deferred {
		t.Errorf("create = %+v, want one (non-deferred) create on node n3 @6 cores with no container name", create)
	}
	if len(grow) != 1 || grow[0].Name != "c-n1" || grow[0].Node != "n1" || grow[0].FromCores != 4 || grow[0].ToCores != 8 || !grow[0].Deferred {
		t.Errorf("grow = %+v, want one deferred grow c-n1 on n1 4->8", grow)
	}
}

// TestRenderPlanText_Infeasible checks the INFEASIBLE section renders the reason, binding and fixes, and
// that a partial create table under an infeasible plan is relabeled as NOT applied (not a bare "create").
func TestRenderPlanText_Infeasible(t *testing.T) {
	d := planData{
		Cluster:         "test-cluster",
		ClusterCapacity: "100TiB",
		Ratio:           "1:0",
		SW:              3, RL: 2, HS: 1,
		MinChunkGiB: 384,
		Plan: &allocator.CapacityPlan{
			Infeasible: "TLC: not enough failure domains",
			Infeasibility: &capacityplanner.InfeasibilityReport{
				Reason:  "TLC: not enough failure domains",
				Pool:    "tlc",
				Binding: "failure domains",
				Fixes:   []string{"add nodes / failure domains that can host a TLC drive container"},
			},
			// A partial TLC placement the planner reached before the pool became infeasible.
			Create: []capacityplanner.NewContainer{
				{Node: "node07", FDValue: "node07", TlcGiB: 3840, Type: "tlc", NumCores: 1},
			},
		},
		Summary: "infeasible",
	}
	out := renderPlanText(&d)
	for _, want := range []string{"FEASIBILITY  INFEASIBLE", "INFEASIBLE", "binding: failure domains", "FIXES:", "add nodes / failure domains", "create (PARTIAL"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan text missing %q\n---\n%s", want, out)
		}
	}
	// The bare, actionable-looking "  create\n" header must NOT appear when infeasible.
	if strings.Contains(out, "\n  create\n") {
		t.Errorf("infeasible plan text shows a bare 'create' header (should be relabeled)\n---\n%s", out)
	}
}

// TestPlanSummary covers the SUMMARY footer for both feasible and infeasible plans.
func TestPlanSummary(t *testing.T) {
	desired := allocator.DesiredCapacity{TlcRawGiB: 23296, QlcRawGiB: 23296}
	create := []capacityplanner.NewContainer{
		{Node: "node07", TlcGiB: 3840, Type: "tlc", NumCores: 1},
		{Node: "node08", TlcGiB: 3840, Type: "tlc", NumCores: 1},
	}

	// Infeasible: QLC blocks; only a partial TLC placement exists — must be flagged not-applied.
	infeasible := &allocator.CapacityPlan{
		Infeasible:    "QLC: not enough failure domains",
		Infeasibility: &capacityplanner.InfeasibilityReport{Pool: "qlc"},
		Create:        create,
	}
	got := planSummary(infeasible, &inventory.Result{}, desired)
	if !strings.HasPrefix(got, "INFEASIBLE") {
		t.Errorf("infeasible summary should lead with INFEASIBLE, got: %q", got)
	}
	for _, want := range []string{"no containers will be created or grown", "Blocking pool: qlc", "will NOT be applied", "target raw"} {
		if !strings.Contains(got, want) {
			t.Errorf("infeasible summary missing %q\n---\n%s", want, got)
		}
	}
	// The misleading feasible-style "create raw +" phrasing must NOT appear.
	if strings.Contains(got, "create raw +") {
		t.Errorf("infeasible summary must not use feasible 'create raw +' phrasing\n---\n%s", got)
	}

	// Infeasible with no partial placement → "No placement could be made."
	empty := &allocator.CapacityPlan{Infeasible: "TLC: capacity bound", Infeasibility: &capacityplanner.InfeasibilityReport{Pool: "tlc"}}
	if got = planSummary(empty, &inventory.Result{}, desired); !strings.Contains(got, "No placement could be made") {
		t.Errorf("infeasible/no-create summary should say no placement could be made, got: %q", got)
	}

	// Feasible: the original "create raw +... target raw ..." format is unchanged.
	feasible := &allocator.CapacityPlan{Create: create}
	got = planSummary(feasible, &inventory.Result{}, desired)
	for _, want := range []string{"create raw +", "across 2 new node(s)", "target raw"} {
		if !strings.Contains(got, want) {
			t.Errorf("feasible summary missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "INFEASIBLE") {
		t.Errorf("feasible summary must not mention INFEASIBLE\n---\n%s", got)
	}
}
