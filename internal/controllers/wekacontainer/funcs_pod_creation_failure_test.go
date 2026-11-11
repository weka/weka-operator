package wekacontainer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/pkg/util/channelerr"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

func TestPodCreationFailureStatus(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status weka.ContainerStatus
		reason string
	}{
		{&lifecycle.StepRunError{Err: lifecycle.NewWaitError(errors.New("feature flags not cached")), Step: &lifecycle.SimpleStep{Name: "GetNodeInfo"}}, weka.PodNotRunning, "EnsurePodWaiting"},
		{errors.New("admission denied"), weka.Error, "EnsurePodError"},
	} {
		r, recorder := newLocalStatusLoop(t, weka.Init, &fakePsExec{})
		if err := r.setPodCreationFailureStatus(context.Background(), "EnsurePod", tc.err); err != nil {
			t.Fatal(err)
		}
		if r.container.Status.Status != tc.status {
			t.Fatalf("status = %s, want %s", r.container.Status.Status, tc.status)
		}
		if ev := <-recorder.Events; !strings.Contains(ev, tc.reason) {
			t.Fatalf("event %q, want reason %s", ev, tc.reason)
		}
	}
}

func TestPodCreationWaitOnChannelFailureUsesChannelHandling(t *testing.T) {
	r, recorder := newLocalStatusLoop(t, weka.Init, &fakePsExec{})
	r.pod = nil
	channelWait := lifecycle.NewWaitError(channelerr.Wrap(errors.New("stream failed")))
	if err := r.setPodCreationFailureStatus(context.Background(), "EnsurePod", channelWait); err != nil {
		t.Fatal(err)
	}
	if _, ok := lastRead(r.container.UID); !ok {
		t.Fatal("channel failure did not reach the unreachable clock")
	}
	if r.container.Status.Status != weka.Init || len(recorder.Events) > 0 {
		t.Fatalf("status = %s, events = %d; want Init and none", r.container.Status.Status, len(recorder.Events))
	}
}

func TestAdhocWithoutPodDeletedAfterStartingTimeout(t *testing.T) {
	prev := config.Config.StuckAdhocPodStartingTimeout
	config.Config.StuckAdhocPodStartingTimeout = 30 * time.Minute
	t.Cleanup(func() { config.Config.StuckAdhocPodStartingTimeout = prev })

	r, recorder := newLocalStatusLoop(t, weka.PodNotRunning, &fakePsExec{})
	r.pod = nil
	r.container.CreationTimestamp = metav1.NewTime(time.Now().Add(-29 * time.Minute))
	if r.adhocStuckTimeoutElapsed() {
		t.Fatal("elapsed before the starting timeout")
	}
	r.container.CreationTimestamp = metav1.NewTime(time.Now().Add(-31 * time.Minute))
	if !r.adhocStuckTimeoutElapsed() {
		t.Fatal("not elapsed after the starting timeout")
	}
	if err := r.deleteStuckAdhocContainer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.container.IsDeletingState() {
		t.Fatalf("state = %s, want deleting", r.container.Spec.State)
	}
	if ev := <-recorder.Events; !strings.Contains(ev, "AdhocPodNotCreated") {
		t.Fatalf("event %q", ev)
	}
}

func TestRecordDriversLoaderWait(t *testing.T) {
	loaderWait := func(age time.Duration) error {
		return lifecycle.NewWaitError(&operations.DriversLoaderNotReadyError{Name: "loader", Status: "Init", Since: time.Now().Add(-age)})
	}
	// Run through a real engine so the error has the shape EnsureDrivers' OnFail receives.
	viaEngine := func(err error) error {
		engine := lifecycle.StepsEngine{Steps: []lifecycle.Step{&lifecycle.SimpleStep{Name: "PollResults", Run: func(context.Context) error { return err }}}}
		return engine.Run(context.Background())
	}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"loader waiting 3m", viaEngine(loaderWait(3 * time.Minute)), true},
		{"loader waiting 1m", viaEngine(loaderWait(time.Minute)), false},
		{"other wait", viaEngine(lifecycle.NewWaitError(errors.New("cannot load drivers"))), false},
	} {
		r, recorder := newLocalStatusLoop(t, weka.Init, &fakePsExec{})
		if err := r.recordDriversLoaderWait(context.Background(), "EnsureDrivers", tc.err); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := len(recorder.Events) > 0; got != tc.want {
			t.Fatalf("%s: event emitted = %v, want %v (err %v)", tc.name, got, tc.want, tc.err)
		}
	}
}
