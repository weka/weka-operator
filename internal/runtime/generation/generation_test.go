package generation

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/runtime/paths"
)

func testRoots(t *testing.T) paths.Roots {
	dir := t.TempDir()
	return paths.Roots{
		K8sRuntime: filepath.Join(dir, "k8s-runtime"),
		HostBinds:  filepath.Join(dir, "host-binds"),
	}
}

func TestIsTakenOver_MissingMarker(t *testing.T) {
	got, err := IsTakenOver(testRoots(t), "1.0")
	if err != nil || got {
		t.Fatalf("got (%v, %v), want (false, nil)", got, err)
	}
}

func TestIsTakenOver_WhitespaceMarker(t *testing.T) {
	p := testRoots(t)
	if err := os.MkdirAll(p.K8sRuntime, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.K8sRuntime, generationFile), []byte("  \n\t"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := IsTakenOver(p, "1.0")
	if err != nil || got {
		t.Fatalf("got (%v, %v), want (false, nil)", got, err)
	}
}

func TestIsTakenOver_Mismatch(t *testing.T) {
	p := testRoots(t)
	if err := os.MkdirAll(p.K8sRuntime, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.K8sRuntime, generationFile), []byte("1.0"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := IsTakenOver(p, "2.0")
	if err != nil || !got {
		t.Fatalf("got (%v, %v), want (true, nil)", got, err)
	}
}

func TestIsTakenOver_Match(t *testing.T) {
	p := testRoots(t)
	if err := os.MkdirAll(p.K8sRuntime, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.K8sRuntime, generationFile), []byte("1.0"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := IsTakenOver(p, "1.0")
	if err != nil || got {
		t.Fatalf("got (%v, %v), want (false, nil)", got, err)
	}
}

func TestAcquireLock_SecondBindFails(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("abstract-namespace unix sockets are Linux-only")
	}
	name := "generation_test_lock"

	first, err := AcquireLock(name)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	defer first.Close()

	if _, err := AcquireLock(name); err == nil {
		t.Fatal("second AcquireLock on held name: got nil error, want failure")
	}
}

func TestPublish(t *testing.T) {
	p := testRoots(t)

	marker, err := Publish(context.Background(), p)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if marker == "" {
		t.Fatal("Publish returned empty marker")
	}

	content, err := os.ReadFile(filepath.Join(p.K8sRuntime, generationFile))
	if err != nil {
		t.Fatalf("reading generation file: %v", err)
	}
	if string(content) != marker {
		t.Fatalf("on-disk generation = %q, want %q", content, marker)
	}

	takenOver, err := IsTakenOver(p, marker)
	if err != nil || takenOver {
		t.Fatalf("IsTakenOver after Publish: got (%v, %v), want (false, nil)", takenOver, err)
	}
}

func TestPublish_WaitsForPersistency(t *testing.T) {
	p := testRoots(t)
	if err := os.MkdirAll(filepath.Join(p.HostBinds, persistBindsSubdir), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := Publish(ctx, p)
		done <- err
	}()

	// Publish must block while the persist-binds dir exists and persistency isn't marked configured.
	select {
	case err := <-done:
		t.Fatalf("Publish returned early (err=%v), want it to block on persistency", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Publish after cancel: got nil error, want context cancellation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Publish did not return after context cancellation")
	}
}
