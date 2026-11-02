package operations

import (
	"testing"
	"time"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
)

func TestNodeIsCsiAccessible(t *testing.T) {
	prev := config.Config.Csi.PreventNewWorkloadOnClientContainerNotRunning
	t.Cleanup(func() { config.Config.Csi.PreventNewWorkloadOnClientContainerNotRunning = prev })

	fresh := metav1.NewTime(time.Now())
	stale := metav1.NewTime(time.Now().Add(-StatsStaleAfter - time.Minute))
	stats := func(at metav1.Time, active int) *weka.WekaContainerMetrics {
		return &weka.WekaContainerMetrics{
			Processes:  weka.EntityStatefulNum{Active: weka.IntMetric(active), Desired: 2},
			LastUpdate: at,
		}
	}

	for name, tc := range map[string]struct {
		status      weka.ContainerStatus
		stats       *weka.WekaContainerMetrics
		internal    string
		preventFlag bool
		nodeDown    bool
		want        bool
	}{
		"fresh, all active":                   {weka.Running, stats(fresh, 2), "", true, false, true},
		"fresh, one down, READY":              {weka.Running, stats(fresh, 1), "READY", true, false, false},
		"stale, READY":                        {weka.Running, stats(stale, 0), "READY", true, false, true},
		"stale, READY, node NotReady":         {weka.Running, stats(stale, 0), "READY", true, true, false},
		"stale, not READY":                    {weka.Running, stats(stale, 2), "NOT_READY", true, false, false},
		"stale, empty internal status":        {weka.Running, stats(stale, 2), "", true, false, false},
		"not running, stale, READY":           {weka.Error, stats(stale, 2), "READY", true, false, false},
		"flag off, not running, stale, READY": {weka.Error, stats(stale, 2), "READY", false, false, false},
		"no stats, READY":                     {weka.Running, nil, "READY", true, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			config.Config.Csi.PreventNewWorkloadOnClientContainerNotRunning = tc.preventFlag
			c := &weka.WekaContainer{}
			c.Status.Status = tc.status
			c.Status.Stats = tc.stats
			c.Status.InternalStatus = tc.internal
			if got := NewCsiTopologyLabelsService("csi.weka.io", "node-1", !tc.nodeDown, c).nodeIsCsiAccessible(); got != tc.want {
				t.Fatalf("nodeIsCsiAccessible = %v, want %v", got, tc.want)
			}
		})
	}
}
