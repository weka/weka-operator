package capacityplanner

import (
	"fmt"
	"strings"
	"testing"
)

// TestListNodes_CapsAtMaxNamedNodes covers the shared primitive every aggregated auto-full-drives warning
// routes through: below the cap every name is spelled out, above it the list is truncated with a
// "(+N more)" tail rather than growing one event into a multi-KB message.
func TestListNodes_CapsAtMaxNamedNodes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
	}{
		{"under cap", autoFullDrivesMaxNamedNodes - 1},
		{"at cap", autoFullDrivesMaxNamedNodes},
		{"over cap", autoFullDrivesMaxNamedNodes + 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var parts []string
			for i := 0; i < tc.count; i++ {
				parts = append(parts, fmt.Sprintf("n%d", i))
			}

			got := listNodes(parts)

			if tc.count <= autoFullDrivesMaxNamedNodes {
				want := strings.Join(parts, ", ")
				if got != want {
					t.Errorf("listNodes() = %q, want %q (no truncation at or under the cap)", got, want)
				}
				return
			}
			for i := 0; i < autoFullDrivesMaxNamedNodes; i++ {
				if !strings.Contains(got, parts[i]) {
					t.Errorf("listNodes() = %q, missing named node %q", got, parts[i])
				}
			}
			if strings.Contains(got, parts[autoFullDrivesMaxNamedNodes]) {
				t.Errorf("listNodes() = %q, must not spell out node %q past the cap", got, parts[autoFullDrivesMaxNamedNodes])
			}
			overflow := tc.count - autoFullDrivesMaxNamedNodes
			wantTail := fmt.Sprintf("(+%d more)", overflow)
			if !strings.HasSuffix(got, wantTail) {
				t.Errorf("listNodes() = %q, want it to end with %q", got, wantTail)
			}
		})
	}
}

// TestFormatPlacementDeferredWarning_EachCauseGetsFullBudget covers the per-cause split: deferred and
// deleting are different causes, each its own Warning and event, so each is capped independently at the
// full autoFullDrivesMaxNamedNodes rather than sharing one budget between them — a fleet with plenty of
// both can legitimately name up to 2x the per-warning maximum in total, split across two Warnings.
func TestFormatPlacementDeferredWarning_EachCauseGetsFullBudget(t *testing.T) {
	var deferred, deleting []string
	for i := 0; i < 12; i++ {
		deferred = append(deferred, fmt.Sprintf("d-%d", i))
		deleting = append(deleting, fmt.Sprintf("x-%d", i))
	}

	warnings := formatPlacementDeferredWarning(deferred, deleting, nil, "")

	if len(warnings) != 2 {
		t.Fatalf("formatPlacementDeferredWarning() returned %d warning(s), want 2 (one per cause): %+v", len(warnings), warnings)
	}

	byCause := map[WarningCause]Warning{}
	for _, w := range warnings {
		byCause[w.Cause] = w
	}

	deferredWarning, ok := byCause[CausePlacementUnscheduled]
	if !ok {
		t.Fatalf("no warning with Cause=%q in %+v", CausePlacementUnscheduled, warnings)
	}
	named := 0
	for _, n := range deferred {
		if strings.Contains(deferredWarning.Message, n) {
			named++
		}
	}
	if named != autoFullDrivesMaxNamedNodes {
		t.Errorf("deferred warning named %d of its own 12 nodes in %q, want the full autoFullDrivesMaxNamedNodes=%d budget",
			named, deferredWarning.Message, autoFullDrivesMaxNamedNodes)
	}
	if !strings.Contains(deferredWarning.Message, "(+") {
		t.Errorf("deferred warning = %q, want it to disclose the truncation with a \"(+N more)\" tail", deferredWarning.Message)
	}
	for _, n := range deleting {
		if strings.Contains(deferredWarning.Message, n) {
			t.Errorf("deferred warning = %q, must not name deleting node %q", deferredWarning.Message, n)
		}
	}

	deletingWarning, ok := byCause[CausePlacementDriveDeleting]
	if !ok {
		t.Fatalf("no warning with Cause=%q in %+v", CausePlacementDriveDeleting, warnings)
	}
	named = 0
	for _, n := range deleting {
		if strings.Contains(deletingWarning.Message, n) {
			named++
		}
	}
	if named != autoFullDrivesMaxNamedNodes {
		t.Errorf("deleting warning named %d of its own 12 nodes in %q, want the full autoFullDrivesMaxNamedNodes=%d budget",
			named, deletingWarning.Message, autoFullDrivesMaxNamedNodes)
	}
	if !strings.Contains(deletingWarning.Message, "(+") {
		t.Errorf("deleting warning = %q, want it to disclose the truncation with a \"(+N more)\" tail", deletingWarning.Message)
	}
	for _, n := range deferred {
		if strings.Contains(deletingWarning.Message, n) {
			t.Errorf("deleting warning = %q, must not name deferred node %q", deletingWarning.Message, n)
		}
	}
}

// TestFormatPlacementDeferredWarning_ComputeBlockedNamesTheBindingDimension covers the wording of the
// compute-blocked cause. The deferral fires on any fit binding (cores, hugepages or memory) and on the
// create path as well as growth, so the clause may only name a dimension when every blocked node agrees on
// one — the same rule autoNodeFitInfeasible applies to Binding.
func TestFormatPlacementDeferredWarning_ComputeBlockedNamesTheBindingDimension(t *testing.T) {
	for _, tc := range []struct {
		name    string
		binding string
		want    string
		reject  string
	}{
		{"agreed on cores", "cores", "holds the cores this placement needs", "hugepages"},
		{"agreed on memory", "memory", "holds the memory this placement needs", "hugepages"},
		{"agreed on hugepages", "hugepages", "holds the hugepages this placement needs", ""},
		{"nodes disagree", "", "holds the resources this placement needs", "hugepages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			warnings := formatPlacementDeferredWarning(nil, nil, []string{"n1"}, tc.binding)
			if len(warnings) != 1 {
				t.Fatalf("want 1 warning, got %+v", warnings)
			}
			msg := warnings[0].Message
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("message must contain %q, got %q", tc.want, msg)
			}
			// The clause must never promise growth: the same deferral covers a create.
			if strings.Contains(msg, "this growth needs") {
				t.Fatalf("message must not say \"growth\" — the create path defers identically, got %q", msg)
			}
			if tc.reject != "" && strings.Contains(msg, tc.reject) {
				t.Fatalf("message must not name %q when it is not the binding, got %q", tc.reject, msg)
			}
		})
	}
}
