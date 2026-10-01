package runtimes

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/process"
)

type fakeRunner struct{}

func (fakeRunner) Run(context.Context, process.Command) (process.Result, error) {
	return process.Result{}, nil
}

func TestRunDiscovery_WritesResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.json")
	cfg := config.DiscoveryConfig{Results: config.Results{Path: path}}

	if err := runDiscovery(context.Background(), &cfg, &Deps{Runner: fakeRunner{}}); err != nil {
		t.Fatalf("runDiscovery: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read results: %v", err)
	}
	var res discoveryResult
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("unmarshal results: %v", err)
	}
	if res.Schema != 1 {
		t.Fatalf("Schema = %d, want 1", res.Schema)
	}
}

func TestDiscoveryResult_ProcVersionKey(t *testing.T) {
	data, err := json.Marshal(discoveryResult{ProcVersion: "Linux version 6.1"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["proc_version"] != "Linux version 6.1" {
		t.Errorf("proc_version = %v", m["proc_version"])
	}
}
