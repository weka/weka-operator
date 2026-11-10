package reconcilerun

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	corev1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/pkg/util/podexec"
)

func run(t *testing.T, stepErr error) (time.Duration, error) {
	t.Helper()
	engine := &lifecycle.StepsEngine{
		StateKeeper: &lifecycle.K8sObject{Object: &corev1.ConfigMap{}},
		Throttler:   throttling.NewSyncMapThrottler(),
		Steps: []lifecycle.Step{&lifecycle.SimpleStep{
			Name: "s",
			Run:  func(context.Context) error { return stepErr },
		}},
	}
	res, err := Run(context.Background(), engine)
	return res.RequeueAfter, err
}

func TestRunChannelErrorFloor(t *testing.T) {
	channel := &podexec.ChannelError{Err: errors.New("stream failed")}
	for _, stepErr := range []error{
		lifecycle.NewWaitError(channel),
		lifecycle.NewWaitErrorWithDuration(channel, 5*time.Second),
		channel,
	} {
		for i := 0; i < 50; i++ {
			got, err := run(t, stepErr)
			if err != nil {
				t.Fatal(err)
			}
			if got < 30*time.Second || got >= 60*time.Second {
				t.Fatalf("RequeueAfter = %v, want [30s, 60s)", got)
			}
		}
	}
}

func TestRunChannelErrorKeepsLongerWait(t *testing.T) {
	got, _ := run(t, lifecycle.NewWaitErrorWithDuration(&podexec.ChannelError{Err: errors.New("x")}, 5*time.Minute))
	if got != 5*time.Minute {
		t.Fatalf("RequeueAfter = %v, want 5m", got)
	}
}

func TestRunOrdinaryErrorsUntouched(t *testing.T) {
	if got, _ := run(t, lifecycle.NewWaitError(errors.New("not ready"))); got != 3*time.Second {
		t.Fatalf("plain wait: %v, want 3s", got)
	}
	if got, _ := run(t, lifecycle.NewWaitErrorWithDuration(errors.New("x"), 15*time.Second)); got != 15*time.Second {
		t.Fatalf("wait with duration: %v, want 15s", got)
	}
	if got, _ := run(t, errors.New("boom")); got != 10*time.Second {
		t.Fatalf("plain error: %v, want 10s", got)
	}
	if got, _ := run(t, nil); got != 30*time.Second {
		t.Fatalf("success: %v, want 30s", got)
	}
}
