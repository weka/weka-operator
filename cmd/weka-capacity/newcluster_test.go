package main

import (
	"testing"

	flags "github.com/jessevdk/go-flags"
)

// TestPlanValidate_ExactlyOneOf covers the --cluster / --new-cluster mutual-exclusion contract.
func TestPlanValidate_ExactlyOneOf(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"neither set", []string{"plan"}, true},
		{"both set", []string{"plan", "--cluster", "c", "--new-cluster", "--cluster-capacity", "30TiB"}, true},
		{"cluster only", []string{"plan", "--cluster", "c"}, false},
		{"new-cluster only", []string{"plan", "--new-cluster", "--cluster-capacity", "30TiB", "--node-selector", "weka.io/x=y"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := parseOpts(t, tc.args)
			err := o.Plan.validate()
			if tc.wantErr && err == nil {
				t.Errorf("validate() = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validate() = %v, want nil", err)
			}
		})
	}
}

// TestPlanValidate_NewClusterRequiredInputs covers --new-cluster's own required inputs.
func TestPlanValidate_NewClusterRequiredInputs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"missing cluster-capacity", []string{"plan", "--new-cluster"}, true},
		{"cluster-capacity set, no node-selector", []string{"plan", "--new-cluster", "--cluster-capacity", "30TiB"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := parseOpts(t, tc.args)
			err := o.Plan.validate()
			if tc.wantErr && err == nil {
				t.Errorf("validate() = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validate() = %v, want nil", err)
			}
		})
	}
}

// TestBuildSyntheticCluster covers the full flag-to-spec population, including the failure-domain label
// and node selector.
func TestBuildSyntheticCluster(t *testing.T) {
	o := parseOpts(t, []string{
		"plan",
		"--new-cluster",
		"--cluster-capacity", "30TiB",
		"--drive-types-ratio", "3:7",
		"--stripe-width", "3",
		"--redundancy", "2",
		"--hot-spare", "1",
		"--node-selector", "weka.io/a=b,weka.io/c=d",
		"--fd-label", "rack",
	})
	cluster, err := o.Plan.buildSyntheticCluster()
	if err != nil {
		t.Fatalf("buildSyntheticCluster() unexpected error: %v", err)
	}
	if cluster.Spec.Dynamic == nil {
		t.Fatalf("Spec.Dynamic is nil")
	}
	if cluster.Spec.Dynamic.ClusterCapacity != "30TiB" {
		t.Errorf("ClusterCapacity = %q, want %q", cluster.Spec.Dynamic.ClusterCapacity, "30TiB")
	}
	if cluster.Spec.Dynamic.DriveTypesRatio == nil || cluster.Spec.Dynamic.DriveTypesRatio.Tlc != 3 || cluster.Spec.Dynamic.DriveTypesRatio.Qlc != 7 {
		t.Errorf("DriveTypesRatio = %+v, want {Tlc:3 Qlc:7}", cluster.Spec.Dynamic.DriveTypesRatio)
	}
	if cluster.Spec.StripeWidth != 3 {
		t.Errorf("StripeWidth = %d, want 3", cluster.Spec.StripeWidth)
	}
	if cluster.Spec.RedundancyLevel != 2 {
		t.Errorf("RedundancyLevel = %d, want 2", cluster.Spec.RedundancyLevel)
	}
	if cluster.Spec.HotSpare != 1 {
		t.Errorf("HotSpare = %d, want 1", cluster.Spec.HotSpare)
	}
	wantSel := map[string]string{"weka.io/a": "b", "weka.io/c": "d"}
	if len(cluster.Spec.NodeSelector) != len(wantSel) {
		t.Errorf("NodeSelector = %v, want %v", cluster.Spec.NodeSelector, wantSel)
	}
	for k, v := range wantSel {
		if cluster.Spec.NodeSelector[k] != v {
			t.Errorf("NodeSelector[%q] = %q, want %q", k, cluster.Spec.NodeSelector[k], v)
		}
	}
	if cluster.Spec.FailureDomain == nil || cluster.Spec.FailureDomain.Label == nil || *cluster.Spec.FailureDomain.Label != "rack" {
		t.Errorf("FailureDomain = %+v, want Label=rack", cluster.Spec.FailureDomain)
	}
}

// TestBuildSyntheticCluster_NoSelectorNoFDLabel covers the defaults when --node-selector and --fd-label
// are both omitted: empty selector (match-all) and nil FailureDomain (AUTO).
func TestBuildSyntheticCluster_NoSelectorNoFDLabel(t *testing.T) {
	o := parseOpts(t, []string{
		"plan",
		"--new-cluster",
		"--cluster-capacity", "30TiB",
	})
	cluster, err := o.Plan.buildSyntheticCluster()
	if err != nil {
		t.Fatalf("buildSyntheticCluster() unexpected error: %v", err)
	}
	if len(cluster.Spec.NodeSelector) != 0 {
		t.Errorf("NodeSelector = %v, want empty", cluster.Spec.NodeSelector)
	}
	if cluster.Spec.FailureDomain != nil {
		t.Errorf("FailureDomain = %+v, want nil (AUTO)", cluster.Spec.FailureDomain)
	}
}

// TestPlanValidate_InvalidChoiceStillParses is a smoke check that parseOpts's no-op CommandHandler pattern
// (from cli_test.go) is being reused correctly here — a valid parse for the plan subcommand with only
// --new-cluster set should succeed at the flags layer even though validate() will separately reject it.
func TestPlanValidate_InvalidChoiceStillParses(t *testing.T) {
	var o options
	p := flags.NewParser(&o, flags.None)
	p.SubcommandsOptional = true
	p.CommandHandler = func(_ flags.Commander, _ []string) error { return nil }
	if _, err := p.ParseArgs([]string{"plan", "--new-cluster"}); err != nil {
		t.Fatalf("ParseArgs: unexpected error: %v", err)
	}
	if err := o.Plan.validate(); err == nil {
		t.Errorf("validate() = nil, want error (missing --cluster-capacity)")
	}
}
