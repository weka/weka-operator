package shutdown

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/paths"
)

func TestReadInstructionsMissingFile(t *testing.T) {
	roots := paths.Roots{HostBinds: t.TempDir(), Tmp: t.TempDir()}
	got := ReadInstructions(roots, "pod1", "boot1")
	if got.AllowStop || got.AllowForceStop {
		t.Fatalf("got %+v, want zero value", got)
	}
}

func TestReadInstructionsMalformedFile(t *testing.T) {
	dir := t.TempDir()
	instrDir := filepath.Join(dir, "shared", "instructions", "pod1", "boot1")
	if err := os.MkdirAll(instrDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(instrDir, "shutdown_instructions.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	roots := paths.Roots{HostBinds: dir, Tmp: t.TempDir()}
	got := ReadInstructions(roots, "pod1", "boot1")
	if got.AllowStop || got.AllowForceStop {
		t.Fatalf("got %+v, want zero value for malformed file", got)
	}
}

func TestReadInstructionsValidFile(t *testing.T) {
	dir := t.TempDir()
	instrDir := filepath.Join(dir, "shared", "instructions", "pod1", "boot1")
	if err := os.MkdirAll(instrDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(Instructions{AllowStop: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(instrDir, "shutdown_instructions.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	roots := paths.Roots{HostBinds: dir, Tmp: t.TempDir()}
	got := ReadInstructions(roots, "pod1", "boot1")
	if !got.AllowStop || got.AllowForceStop {
		t.Fatalf("got %+v, want AllowStop only", got)
	}
}

func TestReadInstructionsTmpMarkerFallback(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, ".allow-force-stop"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	roots := paths.Roots{HostBinds: t.TempDir(), Tmp: tmp}
	got := ReadInstructions(roots, "pod1", "boot1")
	if !got.AllowForceStop {
		t.Fatalf("got %+v, want AllowForceStop from marker", got)
	}
}

func TestReadInstructionsNoPodIDStillHonorsMarkers(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, ".allow-stop"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	roots := paths.Roots{HostBinds: t.TempDir(), Tmp: tmp}
	got := ReadInstructions(roots, "", "")
	if !got.AllowStop {
		t.Fatalf("got %+v, want AllowStop from marker despite empty podID", got)
	}
}

func TestAwaitApprovalDetectsForce(t *testing.T) {
	tmp := t.TempDir()
	roots := paths.Roots{HostBinds: t.TempDir(), Tmp: tmp}
	fc := clock.NewFake(time.Unix(0, 0))

	type res struct {
		force bool
		err   error
	}
	done := make(chan res, 1)
	go func() {
		f, err := AwaitApproval(context.Background(), fc, roots, "pod1", "boot1")
		done <- res{f, err}
	}()

	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(tmp, ".allow-force-stop"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fc.Advance(approvalPollInterval)

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("AwaitApproval() error = %v", r.err)
		}
		if !r.force {
			t.Fatal("force = false, want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AwaitApproval did not return")
	}
}

func TestAwaitApprovalCtxCancel(t *testing.T) {
	roots := paths.Roots{HostBinds: t.TempDir(), Tmp: t.TempDir()}
	fc := clock.NewFake(time.Unix(0, 0))
	ctx, cancel := context.WithCancel(context.Background())

	type res struct {
		force bool
		err   error
	}
	done := make(chan res, 1)
	go func() {
		f, err := AwaitApproval(ctx, fc, roots, "pod1", "boot1")
		done <- res{f, err}
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	fc.Advance(approvalPollInterval)

	select {
	case r := <-done:
		if r.err == nil {
			t.Fatal("err = nil, want ctx.Err()")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AwaitApproval did not return after ctx cancel")
	}
}
