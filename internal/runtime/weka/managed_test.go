package weka

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/process"
)

func TestEnsureManagedContainerSkipsSetupIfPresent(t *testing.T) {
	r := &stubRunner{results: []process.Result{{Stdout: []byte(`[{"name":"envoy"}]`)}}}
	if err := EnsureManagedContainer(context.Background(), r, "envoy", "--no-start", "--disable"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected only the ps lookup, got %d calls: %v", len(r.calls), r.calls)
	}
}

func TestEnsureManagedContainerRunsSetupIfAbsent(t *testing.T) {
	r := &stubRunner{results: []process.Result{{Stdout: []byte(`[]`)}, {}}}
	if err := EnsureManagedContainer(context.Background(), r, "envoy", "--no-start", "--disable"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("expected ps lookup + setup, got %d calls: %v", len(r.calls), r.calls)
	}
	got := r.calls[1]
	want := []string{"local", "setup", "envoy", "--no-start", "--disable"}
	if got.Path != "weka" || len(got.Args) != len(want) {
		t.Fatalf("setup command = %+v, want args %v", got, want)
	}
	for i, a := range want {
		if got.Args[i] != a {
			t.Errorf("arg[%d] = %q, want %q", i, got.Args[i], a)
		}
	}
}

func TestStartManagedContainerRunsBareLocalStart(t *testing.T) {
	r := &stubRunner{}
	if err := StartManagedContainer(context.Background(), r, "envoy"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 || r.calls[0].Path != "weka" || len(r.calls[0].Args) != 2 ||
		r.calls[0].Args[0] != "local" || r.calls[0].Args[1] != "start" {
		t.Fatalf("unexpected command: %+v", r.calls)
	}
}

func TestEnsureContainerExecPollsUntilReady(t *testing.T) {
	r := &stubRunner{errs: []error{errors.New("not ready"), errors.New("not ready"), nil}}
	fc := clock.NewFake(time.Unix(0, 0))

	done := make(chan error, 1)
	go func() { done <- EnsureContainerExec(context.Background(), r, fc, "envoy") }()

	for i := 0; i < 5; i++ {
		time.Sleep(50 * time.Millisecond)
		fc.Advance(2 * time.Second)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("EnsureContainerExec did not return")
	}
	if len(r.calls) != 3 {
		t.Errorf("expected 3 exec attempts, got %d", len(r.calls))
	}
}
