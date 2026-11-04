package wekacontainer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weka/go-steps-engine/throttling"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSetStatusMetricsFailureEvent(t *testing.T) {
	statsAged := func(age time.Duration) *weka.WekaContainerMetrics {
		return &weka.WekaContainerMetrics{LastUpdate: metav1.NewTime(time.Now().Add(-age))}
	}
	for _, tc := range []struct {
		name         string
		stats        *weka.WekaContainerMetrics
		calls, wants int
	}{
		{"stale stats, throttled", statsAged(10 * time.Minute), 3, 1},
		{"recent stats", statsAged(time.Minute), 1, 0},
		{"no stats yet", nil, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := events.NewFakeRecorder(10)
			r := &containerReconcilerLoop{
				Recorder:      recorder,
				ThrottlingMap: throttling.NewSyncMapThrottler(),
				container: &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default"},
					Status:     weka.WekaContainerStatus{Stats: tc.stats},
				},
			}

			for range tc.calls {
				if err := r.SetStatusMetrics(context.Background()); err == nil {
					t.Fatal("expected an error with no pod set")
				}
			}

			if len(recorder.Events) != tc.wants {
				t.Fatalf("got %d events, want %d", len(recorder.Events), tc.wants)
			}
			if tc.wants > 0 {
				if ev := <-recorder.Events; !strings.Contains(ev, "Warning StatusMetricsFailed") {
					t.Fatalf("event = %q", ev)
				}
			}
		})
	}
}

func TestPublishContainerStatsKeepsTheSampleWhenMountsFail(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default"},
		Spec:       weka.WekaContainerSpec{Mode: weka.WekaContainerModeClient},
		Status: weka.WekaContainerStatus{Stats: &weka.WekaContainerMetrics{
			ActiveMounts: 3,
			LastUpdate:   metav1.NewTime(time.Now().Add(-time.Hour)),
		}},
	}
	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(container).WithStatusSubresource(container).Build()
	mountsErr := errors.New("mounts unavailable")
	r := &containerReconcilerLoop{Client: fc, container: container, activeMountsErr: mountsErr}

	sampledAt := time.Now().Add(-10 * time.Minute)
	sample := &weka.WekaContainerMetrics{DriverFrontends: weka.EntityStatefulNum{Active: 0, Created: 1}}
	if err := r.publishContainerStats(context.Background(), sample, sampledAt); !errors.Is(err, mountsErr) {
		t.Fatalf("err = %v, want the mount error", err)
	}

	stored := &weka.WekaContainer{}
	if err := fc.Get(context.Background(), client.ObjectKeyFromObject(container), stored); err != nil {
		t.Fatal(err)
	}
	got := stored.Status.Stats
	if got == nil || got.LastUpdate.Time.Unix() != sampledAt.Unix() {
		t.Fatalf("the sample was not published and stamped with its sample time: %+v", got)
	}
	if got.DriverFrontends.Created != 1 || got.ActiveMounts != 3 {
		t.Fatalf("got frontends %+v, mounts %d; want the new frontends and the previous mount count", got.DriverFrontends, got.ActiveMounts)
	}
}
