package config

import "testing"

// TestGetCleanupRemovedNodesMode covers the CLEANUP_REMOVED_NODES env var parsing:
// "false" -> Off, "true" -> On, "auto" -> Auto, unset/empty -> Auto (shipped default),
// and any other non-empty (invalid) value fails closed to Off rather than silently
// enabling cleanup. Values are trimmed and lower-cased before matching.
func TestGetCleanupRemovedNodesMode(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want CleanupRemovedNodesMode
	}{
		{name: "false", env: "false", want: CleanupRemovedNodesOff},
		{name: "true", env: "true", want: CleanupRemovedNodesOn},
		{name: "auto", env: "auto", want: CleanupRemovedNodesAuto},
		{name: "empty defaults to auto", env: "", want: CleanupRemovedNodesAuto},
		{name: "uppercase AUTO normalizes to auto", env: "AUTO", want: CleanupRemovedNodesAuto},
		{name: "padded auto normalizes to auto", env: " auto ", want: CleanupRemovedNodesAuto},
		{name: "garbage fails closed to off", env: "garbage", want: CleanupRemovedNodesOff},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CLEANUP_REMOVED_NODES", tt.env)
			got := getCleanupRemovedNodesMode()
			if got != tt.want {
				t.Errorf("getCleanupRemovedNodesMode() with CLEANUP_REMOVED_NODES=%q = %q, want %q", tt.env, got, tt.want)
			}
		})
	}
}

// TestCleanupRemovedNodesMode_CleansOnNodeRemoval verifies which modes intend eventual
// cleanup of a removed node's backend container: Off never does, On and Auto do.
func TestCleanupRemovedNodesMode_CleansOnNodeRemoval(t *testing.T) {
	tests := []struct {
		name string
		mode CleanupRemovedNodesMode
		want bool
	}{
		{name: "off", mode: CleanupRemovedNodesOff, want: false},
		{name: "on", mode: CleanupRemovedNodesOn, want: true},
		{name: "auto", mode: CleanupRemovedNodesAuto, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.mode.CleansOnNodeRemoval(); got != tt.want {
				t.Errorf("%s.CleansOnNodeRemoval() = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}
