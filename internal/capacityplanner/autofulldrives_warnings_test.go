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

// TestFormatPlacementDeferredWarning_SharesBudgetAcrossCauses guards against spending the cap twice: with
// both causes present, deferred and deleting must split one autoFullDrivesMaxNamedNodes budget rather than
// each getting the full cap, or a fleet with plenty of both would name up to 2x the intended maximum.
func TestFormatPlacementDeferredWarning_SharesBudgetAcrossCauses(t *testing.T) {
	var deferred, deleting []string
	for i := 0; i < 12; i++ {
		deferred = append(deferred, fmt.Sprintf("d-%d", i))
		deleting = append(deleting, fmt.Sprintf("x-%d", i))
	}

	w := formatPlacementDeferredWarning(deferred, deleting)

	named := 0
	for _, n := range deferred {
		if strings.Contains(w.Message, n) {
			named++
		}
	}
	for _, n := range deleting {
		if strings.Contains(w.Message, n) {
			named++
		}
	}
	if named > autoFullDrivesMaxNamedNodes {
		t.Errorf("formatPlacementDeferredWarning() named %d nodes across both causes in %q, want at most autoFullDrivesMaxNamedNodes=%d total",
			named, w.Message, autoFullDrivesMaxNamedNodes)
	}
	if !strings.Contains(w.Message, "(+") {
		t.Errorf("formatPlacementDeferredWarning() = %q, want it to disclose the truncation with a \"(+N more)\" tail", w.Message)
	}
}
