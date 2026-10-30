package wekacontainer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/weka/go-steps-engine/throttling"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
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
