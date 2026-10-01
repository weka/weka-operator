package runtimes

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/weka/weka-operator/internal/runtime/config"
)

func TestRunAdhoc_NoInstructions(t *testing.T) {
	err := runAdhoc(context.Background(), &config.AdhocConfig{}, &Deps{Runner: fakeRunner{}})
	if err == nil {
		t.Fatal("runAdhoc: want error for missing instructions, got nil")
	}
}

func TestRunAdhoc_UnknownInstruction(t *testing.T) {
	cfg := config.AdhocConfig{Operation: config.Operation{Raw: "{}", Type: "bogus"}}
	err := runAdhoc(context.Background(), &cfg, &Deps{Runner: fakeRunner{}})
	if err == nil {
		t.Fatal("runAdhoc: want error for unknown instruction type, got nil")
	}
}

func TestRunAdhoc_KnownInstruction(t *testing.T) {
	cfg := config.AdhocConfig{
		Operation: config.Operation{Raw: "{}", Type: "umount"},
		Results:   config.Results{Path: filepath.Join(t.TempDir(), "result.json")},
	}
	if err := runAdhoc(context.Background(), &cfg, &Deps{Runner: fakeRunner{}}); err != nil {
		t.Fatalf("runAdhoc: %v", err)
	}
}
