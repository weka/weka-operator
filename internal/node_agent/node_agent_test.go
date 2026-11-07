package node_agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const driverInterfaceConnected = `IO node version c5a7e72cdb528b5c
mount:[wc-18a057661657client:sbid-0x3] default -o,writecache,sync_on_close,container_name=18a057661657client
GW driver_state: DRIVER_ACCEPTING
Active mounts: 1
Container=18a057661657client FE 0: Connected frontend pid 1417198
max_dirty_pages: 262144, dirty_pages_count: 0, writeback_pages_count: 0
`

const driverInterfaceStopped = `IO node version c5a7e72cdb528b5c
GW driver_state: DRIVER_ACCEPTING
Active mounts: 1
Container=18a057661657client FE 0: Frontend is not connected
`

func TestParseDriverInterface(t *testing.T) {
	for name, tc := range map[string]struct {
		data          string
		container     string
		wantMounts    int
		wantConnected bool
	}{
		"connected":                   {driverInterfaceConnected, "18a057661657client", 1, true},
		"other container connected":   {driverInterfaceConnected, "18a057661657", 1, false},
		"frontend not connected":      {driverInterfaceStopped, "18a057661657client", 1, false},
		"no frontend line":            {"Active mounts: 0\nNew mounts are allowed\n", "18a057661657client", 0, false},
		"one connected, one not":      {driverInterfaceConnected + "Container=18a057661657client FE 1: Frontend is not connected\n", "18a057661657client", 1, false},
		"two connected":               {driverInterfaceConnected + "Container=18a057661657client FE 1: Connected frontend pid 5\n", "18a057661657client", 1, true},
		"no container name requested": {driverInterfaceConnected, "", 1, false},
	} {
		t.Run(name, func(t *testing.T) {
			mounts, connected, err := parseDriverInterface(strings.NewReader(tc.data), tc.container)
			if err != nil {
				t.Fatal(err)
			}
			if mounts != tc.wantMounts || connected != tc.wantConnected {
				t.Fatalf("got mounts=%d connected=%v, want mounts=%d connected=%v", mounts, connected, tc.wantMounts, tc.wantConnected)
			}
		})
	}
}

func TestReadDriverInterfaceNoFile(t *testing.T) {
	_, _, err := readDriverInterface(filepath.Join(t.TempDir(), "interface"), "18a057661657client")
	if !os.IsNotExist(err) {
		t.Fatalf("err = %v, want not-exist", err)
	}
}

func TestReadActiveMounts(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const name = "18a057661657client"
	missing := filepath.Join(dir, "missing")
	global := write("global", driverInterfaceConnected)

	t.Run("per-container missing, global connected", func(t *testing.T) {
		got, err := readActiveMounts(missing, global, name)
		if err != nil {
			t.Fatal(err)
		}
		if got.ActiveMounts != 0 || got.FrontendConnected == nil || !*got.FrontendConnected {
			t.Fatalf("unexpected response %+v", got)
		}
	})
	t.Run("both missing", func(t *testing.T) {
		if _, err := readActiveMounts(missing, missing, name); !os.IsNotExist(err) {
			t.Fatalf("expected not-exist error, got %v", err)
		}
	})
	t.Run("per-container ok, global missing", func(t *testing.T) {
		perContainer := write("per", driverInterfaceStopped)
		got, err := readActiveMounts(perContainer, missing, name)
		if err != nil {
			t.Fatal(err)
		}
		if got.ActiveMounts != 1 || got.FrontendConnected != nil {
			t.Fatalf("unexpected response %+v", got)
		}
	})
	t.Run("global only", func(t *testing.T) {
		got, err := readActiveMounts("", global, name)
		if err != nil {
			t.Fatal(err)
		}
		if got.ActiveMounts != 1 || got.FrontendConnected == nil || !*got.FrontendConnected {
			t.Fatalf("unexpected response %+v", got)
		}
	})
}
