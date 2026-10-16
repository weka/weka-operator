package weka

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/runtime/process"
)

func TestGetContainersThenFindContainerByName(t *testing.T) {
	tests := []struct {
		name    string
		psJSON  string
		target  string
		wantNil bool
		wantErr bool
	}{
		{name: "found", psJSON: `[{"name":"ssdproxy","isRunning":true}]`, target: "ssdproxy"},
		{name: "not found", psJSON: `[{"name":"envoy"}]`, target: "ssdproxy", wantNil: true},
		{name: "invalid json", psJSON: `not json`, target: "ssdproxy", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &stubRunner{results: []process.Result{{Stdout: []byte(tt.psJSON)}}}
			containers, err := GetContainers(context.Background(), r)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			got, _ := findContainerByName(containers, tt.target)
			if (got == nil) != tt.wantNil {
				t.Fatalf("got = %v, wantNil %v", got, tt.wantNil)
			}
		})
	}
}

func TestRecoverExistingSSDProxyContainerRunningIsNoop(t *testing.T) {
	err := recoverExistingSSDProxyContainer(context.Background(), map[string]interface{}{"isRunning": true}, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRecoverExistingSSDProxyContainerNonUnknownStatusIsNoop(t *testing.T) {
	err := recoverExistingSSDProxyContainer(context.Background(), map[string]interface{}{"runStatus": "Stopped"}, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRecoverExistingSSDProxyContainerNonEmptyResourcesIsNoop(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "resources.json"), []byte(`{"memory":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := recoverExistingSSDProxyContainer(context.Background(), map[string]interface{}{"runStatus": "Unknown"}, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRecoverExistingSSDProxyContainerNoCandidateErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "resources.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	err := recoverExistingSSDProxyContainer(context.Background(), map[string]interface{}{"runStatus": "Unknown"}, dir)
	if !errors.Is(err, ErrUnsupportedSSDProxyRecovery) {
		t.Fatalf("err = %v, want ErrUnsupportedSSDProxyRecovery", err)
	}
}

func TestRecoverExistingSSDProxyContainerRelinksNewestCandidate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "resources.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(dir, "weka-resources.1.json")
	newer := filepath.Join(dir, "weka-resources.2.json")
	if err := os.WriteFile(older, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(older, now, now.Add(-1*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, now, now); err != nil {
		t.Fatal(err)
	}

	err := recoverExistingSSDProxyContainer(context.Background(), map[string]interface{}{"runStatus": "Unknown"}, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	link, err := os.Readlink(filepath.Join(dir, "resources.json"))
	if err != nil {
		t.Fatalf("resources.json is not a symlink: %v", err)
	}
	if link != "weka-resources.2.json" {
		t.Errorf("resources.json links to %q, want the newest candidate weka-resources.2.json", link)
	}
}
