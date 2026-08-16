package services

import "testing"

// TestIsPostInitFrozenErr pins the exact weka messages the frozen-config tolerance depends on. Both
// strings are external and version-dependent: a weka release that rewords either one silently
// reverts the tolerance and re-introduces the permanent FormCluster wedge it exists to prevent, so
// they are asserted here rather than left implicit in the matcher.
//
// Both tolerated messages were captured from a live cluster (exit 50), one per flag — the wording
// differs between them, which is why the matcher keys on the shared half only.
func TestIsPostInitFrozenErr(t *testing.T) {
	tolerated := []struct {
		name   string
		stderr string
	}{
		{
			name:   "--parity-drives / --data-drives",
			stderr: "error: Clustering operation failed: Can't change RAID drives configuration after the cluster has been initialized - you'll need to factory reset all the hosts\n\x00",
		},
		{
			name:   "--bucket-raft-size",
			stderr: "error: Clustering operation failed: Can't change Raft size configuration after the cluster has been initialized\n\x00",
		},
	}
	for _, c := range tolerated {
		if !isPostInitFrozenErr(c.stderr) {
			t.Errorf("%s: must be tolerated, otherwise FormCluster wedges forever", c.name)
		}
	}

	// Everything else must still fail the step. Exit 50 is the generic "Clustering operation failed"
	// class, so a rejected value shares the prefix of a tolerated message and must not be swallowed.
	rejected := []struct {
		name   string
		stderr string
	}{
		{"rejected value", "error: Clustering operation failed: Invalid parity drives value 9"},
		{"auth failure", "error: Not authenticated"},
		{"unrelated clustering failure", "error: Clustering operation failed: host is not part of the cluster"},
		{"empty stderr", ""},
	}
	for _, c := range rejected {
		if isPostInitFrozenErr(c.stderr) {
			t.Errorf("%s: must not be tolerated", c.name)
		}
	}
}
