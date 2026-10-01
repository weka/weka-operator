package runtimes

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/paths"
)

func TestRunPreRunScript_Empty(t *testing.T) {
	if err := runPreRunScript(context.Background(), &Deps{Runner: fakeRunner{}}, ""); err != nil {
		t.Fatalf("runPreRunScript: want nil for empty script, got %v", err)
	}
}

func TestRunPreRunScript_BadBase64(t *testing.T) {
	err := runPreRunScript(context.Background(), &Deps{Runner: fakeRunner{}}, "not-valid-base64!!")
	if err == nil {
		t.Fatal("runPreRunScript: want error for invalid base64, got nil")
	}
}

func TestRunPreRunScript_Runs(t *testing.T) {
	// "echo hi" base64-encoded.
	encoded := "ZWNobyBoaQ=="
	deps := &Deps{Runner: fakeRunner{}, Paths: paths.Roots{Tmp: t.TempDir()}}
	if err := runPreRunScript(context.Background(), deps, encoded); err != nil {
		t.Fatalf("runPreRunScript: %v", err)
	}
}

// TestBindAndPublish_BindFailure_NoResultsFile verifies that when the serve port is already
// taken, no results file is written: results.Write must never run before a successful bind.
func TestBindAndPublish_BindFailure_NoResultsFile(t *testing.T) {
	occupied, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("pre-occupy a port: %v", err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps := &Deps{Coord: testCoordinator(ctx)}
	resultsPath := filepath.Join(t.TempDir(), "results.json")

	_, logger := instrumentation.CreateLogSpan(ctx, "test")
	defer logger.End()

	err = bindAndPublish(ctx, deps, port, resultsPath, &builderResult{DriverBuilt: true}, logger)
	if err == nil {
		t.Fatal("bindAndPublish: want error for already-bound port, got nil")
	}
	if _, statErr := os.Stat(resultsPath); !os.IsNotExist(statErr) {
		t.Errorf("results file exists after bind failure: stat err = %v", statErr)
	}
}

// TestBindAndPublish_Succeeds_WritesResultsFile verifies the happy path: a free port binds,
// results are written, and the HTTP server task is handed to the coordinator.
func TestBindAndPublish_Succeeds_WritesResultsFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps := &Deps{Coord: testCoordinator(ctx)}
	resultsPath := filepath.Join(t.TempDir(), "results.json")

	_, logger := instrumentation.CreateLogSpan(ctx, "test")
	defer logger.End()

	if err := bindAndPublish(ctx, deps, 0, resultsPath, &builderResult{DriverBuilt: true, WekaVersion: "4.4.0"}, logger); err != nil {
		t.Fatalf("bindAndPublish: %v", err)
	}
	if _, err := os.Stat(resultsPath); err != nil {
		t.Errorf("results file missing after successful bind: %v", err)
	}
}
