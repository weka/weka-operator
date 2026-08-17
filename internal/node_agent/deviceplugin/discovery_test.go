package deviceplugin

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiscoverNumaRegions(t *testing.T) {
	dir := t.TempDir()

	// Valid NUMA node directories.
	for _, name := range []string{"node0", "node1", "node3"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0755); err != nil {
			t.Fatalf("failed to create %s: %v", name, err)
		}
	}

	// Junk: unrelated directory, non-numeric suffix directory, and a plain file that looks
	// like a node directory.
	if err := os.Mkdir(filepath.Join(dir, "cpu0"), 0755); err != nil {
		t.Fatalf("failed to create cpu0: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "nodex"), 0755); err != nil {
		t.Fatalf("failed to create nodex: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node2"), []byte("not a directory"), 0644); err != nil {
		t.Fatalf("failed to create node2 file: %v", err)
	}

	regions, err := DiscoverNumaRegions(dir)
	if err != nil {
		t.Fatalf("DiscoverNumaRegions returned error: %v", err)
	}

	want := []int{0, 1, 3}
	if !reflect.DeepEqual(regions, want) {
		t.Errorf("DiscoverNumaRegions() = %v, want %v", regions, want)
	}
}

func TestDiscoverNumaRegions_MissingDir(t *testing.T) {
	regions, err := DiscoverNumaRegions(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("expected no error for missing dir, got: %v", err)
	}
	if len(regions) != 0 {
		t.Errorf("expected no regions for missing dir, got %v", regions)
	}
}

func TestNumaNodeDirFromSysfsRoot(t *testing.T) {
	got := numaNodeDirFromSysfsRoot("/sys")
	want := filepath.Join("/sys", "devices", "system", "node")
	if got != want {
		t.Errorf("numaNodeDirFromSysfsRoot(/sys) = %q, want %q", got, want)
	}
}
