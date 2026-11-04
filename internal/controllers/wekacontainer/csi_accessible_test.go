package wekacontainer

import (
	"fmt"
	"sync"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

const testBootID = "boot-1"

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func healthyStats(at time.Time) *weka.WekaContainerMetrics {
	return &weka.WekaContainerMetrics{
		Processes:         weka.EntityStatefulNum{Active: 3, Created: 3},
		ProcessesPulledAt: metav1.NewTime(at),
		BootID:            testBootID,
		DriverFrontends:   weka.EntityStatefulNum{Active: 1, Created: 1},
		LastUpdate:        metav1.NewTime(at),
	}
}

func baseInput(tracker *unreachableTracker) csiAccessibleInput {
	return csiAccessibleInput{
		now:                      epoch,
		nodeReady:                true,
		nodeBootID:               testBootID,
		csiPod:                   csiPodHealthy,
		statsFreshness:           2 * time.Minute,
		unknownTimeout:           5 * time.Minute,
		systemicFailureThreshold: 80,
		fetchFrontend:            func() frontendState { panic("must not be called") },
		tracker:                  tracker,
		trackerKey:               "ns/client",
		node:                     "node-1",
	}
}

func TestDecideCsiAccessibleSignals(t *testing.T) {
	unreachable := func() frontendState { return frontendUnknown }
	for name, tc := range map[string]struct {
		mutate func(*csiAccessibleInput)
		want   accessibleDecision
	}{
		"boot id stale beats healthy stats": {func(in *csiAccessibleInput) {
			in.driversBootIDStale = true
			in.stats = healthyStats(epoch)
		}, accessibleRemove},
		"node not ready beats healthy stats": {func(in *csiAccessibleInput) {
			in.nodeReady = false
			in.stats = healthyStats(epoch)
		}, accessibleRemove},
		"client pod gone beats healthy stats": {func(in *csiAccessibleInput) {
			in.clientPodGone = true
			in.stats = healthyStats(epoch)
		}, accessibleRemove},
		"csi pod missing beats healthy stats": {func(in *csiAccessibleInput) {
			in.csiPod = csiPodUnhealthy
			in.stats = healthyStats(epoch)
		}, accessibleRemove},
		"csi pod state unknown is not a signal": {func(in *csiAccessibleInput) {
			in.csiPod = csiPodUnknown
			in.stats = healthyStats(epoch)
		}, accessibleSet},
		"weka reports not serving": {func(in *csiAccessibleInput) {
			in.wekaNotServing = true
			in.stats = healthyStats(epoch)
		}, accessibleRemove},
		"fresh stats, processes down": {func(in *csiAccessibleInput) {
			s := healthyStats(epoch)
			s.Processes.Active = 2
			in.stats = s
		}, accessibleRemove},
		"fresh stats, frontend down": {func(in *csiAccessibleInput) {
			s := healthyStats(epoch)
			s.DriverFrontends.Active = 0
			in.stats = s
		}, accessibleRemove},
		"fresh by age but older than the node Ready transition falls back to http": {func(in *csiAccessibleInput) {
			in.stats = healthyStats(epoch.Add(-time.Minute))
			in.readySince = epoch.Add(-30 * time.Second)
			in.fetchFrontend = func() frontendState { return frontendNotConnected }
		}, accessibleRemove},
		"stats newer than the node Ready transition are used": {func(in *csiAccessibleInput) {
			in.stats = healthyStats(epoch.Add(-time.Minute))
			in.readySince = epoch.Add(-2 * time.Minute)
		}, accessibleSet},
		"stale process pull with processes down is not a removal signal": {func(in *csiAccessibleInput) {
			s := healthyStats(epoch)
			s.Processes.Active = 1
			s.ProcessesPulledAt = metav1.NewTime(epoch.Add(-3 * time.Minute))
			in.stats = s
			in.fetchFrontend = func() frontendState { return frontendConnected }
		}, accessibleSet},
		"stale process pull with healthy frontends falls back to http": {func(in *csiAccessibleInput) {
			s := healthyStats(epoch)
			s.ProcessesPulledAt = metav1.NewTime(epoch.Add(-3 * time.Minute))
			in.stats = s
			in.fetchFrontend = func() frontendState { return frontendNotConnected }
		}, accessibleRemove},
		"stale process pull, frontend down still removes": {func(in *csiAccessibleInput) {
			s := healthyStats(epoch)
			s.ProcessesPulledAt = metav1.NewTime(epoch.Add(-3 * time.Minute))
			s.DriverFrontends.Active = 0
			in.stats = s
		}, accessibleRemove},
		"old node-agent without a process pull time falls back to http": {func(in *csiAccessibleInput) {
			s := healthyStats(epoch)
			s.ProcessesPulledAt = metav1.Time{}
			in.stats = s
			in.fetchFrontend = func() frontendState { return frontendNotConnected }
		}, accessibleRemove},
		"stats sampled under another boot fall back to http": {func(in *csiAccessibleInput) {
			s := healthyStats(epoch)
			s.BootID = "boot-0"
			in.stats = s
			in.fetchFrontend = func() frontendState { return frontendNotConnected }
		}, accessibleRemove},
		"stats from an old node-agent without a boot id fall back to http": {func(in *csiAccessibleInput) {
			s := healthyStats(epoch)
			s.BootID = ""
			in.stats = s
			in.fetchFrontend = func() frontendState { return frontendConnected }
		}, accessibleSet},
		"process pull older than the node Ready transition is not a removal signal": {func(in *csiAccessibleInput) {
			s := healthyStats(epoch)
			s.Processes.Active = 1
			s.ProcessesPulledAt = metav1.NewTime(epoch.Add(-time.Minute))
			in.stats = s
			in.readySince = epoch.Add(-30 * time.Second)
			in.fetchFrontend = func() frontendState { return frontendConnected }
		}, accessibleSet},
		"fresh stats healthy, no http": {func(in *csiAccessibleInput) {
			in.stats = healthyStats(epoch.Add(-time.Minute))
		}, accessibleSet},
		"stale stats, http connected": {func(in *csiAccessibleInput) {
			in.stats = healthyStats(epoch.Add(-3 * time.Minute))
			in.fetchFrontend = func() frontendState { return frontendConnected }
		}, accessibleSet},
		"stats disabled, http connected": {func(in *csiAccessibleInput) {
			in.fetchFrontend = func() frontendState { return frontendConnected }
		}, accessibleSet},
		"old node-agent without frontend counts falls back to http": {func(in *csiAccessibleInput) {
			s := healthyStats(epoch)
			s.DriverFrontends = weka.EntityStatefulNum{}
			in.stats = s
			in.fetchFrontend = func() frontendState { return frontendNotConnected }
		}, accessibleRemove},
		"http not connected": {func(in *csiAccessibleInput) {
			in.fetchFrontend = func() frontendState { return frontendNotConnected }
		}, accessibleRemove},
		"http unreachable within timeout keeps": {func(in *csiAccessibleInput) {
			in.fetchFrontend = unreachable
		}, accessibleKeep},
		"node-agent without the field keeps": {func(in *csiAccessibleInput) {
			in.fetchFrontend = func() frontendState { return frontendUnknown }
		}, accessibleKeep},
	} {
		t.Run(name, func(t *testing.T) {
			in := baseInput(&unreachableTracker{})
			tc.mutate(&in)
			if got, reason := decideCsiAccessible(&in); got != tc.want {
				t.Fatalf("got %v (%s), want %v", got, reason, tc.want)
			}
		})
	}
}

func TestDecideCsiAccessibleUnknownTimeout(t *testing.T) {
	in := baseInput(&unreachableTracker{})
	in.fetchFrontend = func() frontendState { return frontendUnknown }

	if got, _ := decideCsiAccessible(&in); got != accessibleKeep {
		t.Fatalf("first failure: got %v, want keep", got)
	}
	in.now = epoch.Add(4 * time.Minute)
	if got, _ := decideCsiAccessible(&in); got != accessibleKeep {
		t.Fatalf("within timeout: got %v, want keep", got)
	}
	in.now = epoch.Add(5 * time.Minute)
	if got, reason := decideCsiAccessible(&in); got != accessibleRemove {
		t.Fatalf("past timeout, below threshold: got %v (%s), want remove", got, reason)
	}

	in.fetchFrontend = func() frontendState { return frontendConnected }
	if got, _ := decideCsiAccessible(&in); got != accessibleSet {
		t.Fatalf("recovered: got %v, want set", got)
	}
	in.fetchFrontend = func() frontendState { return frontendUnknown }
	if got, _ := decideCsiAccessible(&in); got != accessibleKeep {
		t.Fatalf("a success clears the failure streak: got %v, want keep", got)
	}
}

// observePeers records the latest evaluation of the client's other nodes.
func observePeers(tr *unreachableTracker, at time.Time, unknown, healthy int) {
	for i := 0; i < unknown; i++ {
		tr.observe("ns/client", fmt.Sprintf("unknown-%d", i), at, true)
	}
	for i := 0; i < healthy; i++ {
		tr.observe("ns/client", fmt.Sprintf("healthy-%d", i), at, false)
	}
}

func TestDecideCsiAccessibleSystemic(t *testing.T) {
	for name, tc := range map[string]struct {
		unknownPeers, healthyPeers int
		want                       accessibleDecision
	}{
		"7 of 8 unknown":                  {6, 1, accessibleKeep},
		"4 of 5 unknown is the threshold": {3, 1, accessibleKeep},
		"4 of 6 unknown":                  {3, 2, accessibleRemove},
		"1 of 8 unknown":                  {0, 7, accessibleRemove},
		"1 unknown and 2 healthy: the other 5 nodes went NotReady and are no longer tracked": {0, 2, accessibleRemove},
		"2 of 2 unknown is too few to be systemic":                                           {1, 0, accessibleRemove},
	} {
		t.Run(name, func(t *testing.T) {
			tracker := &unreachableTracker{}
			in := baseInput(tracker)
			in.fetchFrontend = func() frontendState { return frontendUnknown }
			decideCsiAccessible(&in) // node-1 starts failing
			in.now = epoch.Add(6 * time.Minute)
			observePeers(tracker, in.now, tc.unknownPeers, tc.healthyPeers)
			if got, reason := decideCsiAccessible(&in); got != tc.want {
				t.Fatalf("got %v (%s), want %v", got, reason, tc.want)
			}
		})
	}
}

func TestUnreachableTrackerConcurrent(t *testing.T) {
	tr := &unreachableTracker{}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			node := fmt.Sprintf("n%d", i%5)
			for j := 0; j < 100; j++ {
				tr.observe("c", node, epoch.Add(time.Duration(j)*time.Second), j%2 == 0)
				tr.share("c", epoch, time.Minute)
				if j%10 == 0 {
					tr.clear("c", node)
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestCsiContainerNotServing(t *testing.T) {
	prev := config.Config.Csi.PreventNewWorkloadOnClientContainerNotRunning
	t.Cleanup(func() { config.Config.Csi.PreventNewWorkloadOnClientContainerNotRunning = prev })

	for name, tc := range map[string]struct {
		status   weka.ContainerStatus
		internal string
		flag     bool
		want     bool
	}{
		"running and READY":                        {weka.Running, "READY", true, false},
		"running, internal status not yet read":    {weka.Running, "", true, false},
		"running but not READY":                    {weka.Running, "STEM", true, true},
		"running but not READY, flag off":          {weka.Running, "STEM", false, true},
		"stopped":                                  {weka.Stopped, "READY", true, true},
		"stopped, flag off":                        {weka.Stopped, "READY", false, false},
		"starting":                                 {weka.Starting, "", true, true},
		"unhealthy":                                {weka.Unhealthy, "", true, true},
		"pod not running":                          {weka.PodNotRunning, "", true, true},
		"operator Error with a READY reply":        {weka.Error, "READY", true, false},
		"operator Error with no reply":             {weka.Error, "", true, false},
		"operator Error, stale non-READY reply":    {weka.Error, "STEM", true, true},
		"weka cannot read its own state":           {"Unknown", "READY", true, false},
		"weka cannot read its own state, no READY": {"Unknown", "", true, false},
	} {
		config.Config.Csi.PreventNewWorkloadOnClientContainerNotRunning = tc.flag
		c := &weka.WekaContainer{}
		c.Status.Status = tc.status
		c.Status.InternalStatus = tc.internal
		if got := csiContainerNotServing(c); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

func csiPod(node string, ready bool) v1.Pod {
	status := v1.ConditionFalse
	if ready {
		status = v1.ConditionTrue
	}
	return v1.Pod{
		Spec:   v1.PodSpec{NodeName: node},
		Status: v1.PodStatus{Conditions: []v1.PodCondition{{Type: v1.PodReady, Status: status}}},
	}
}

func TestCsiPodStateOf(t *testing.T) {
	terminating := csiPod("n1", true)
	now := metav1.Now()
	terminating.DeletionTimestamp = &now

	for name, tc := range map[string]struct {
		pods []v1.Pod
		want csiPodState
	}{
		"no pod":                    {nil, csiPodUnhealthy},
		"ready pod":                 {[]v1.Pod{csiPod("n1", true)}, csiPodHealthy},
		"not ready pod":             {[]v1.Pod{csiPod("n1", false)}, csiPodUnhealthy},
		"terminating ready pod":     {[]v1.Pod{terminating}, csiPodUnhealthy},
		"terminating and a new one": {[]v1.Pod{terminating, csiPod("n1", true)}, csiPodHealthy},
	} {
		if got := csiPodStateOf(tc.pods); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

func TestDecideCsiAccessibleStaggeredStreaks(t *testing.T) {
	failing := func() frontendState { return frontendUnknown }
	names := []string{"n1", "n2", "n3", "n4", "n5", "n6", "n7", "n8"}

	t.Run("all nodes unknown with streaks 20s apart: each is kept at its own timeout", func(t *testing.T) {
		tracker := &unreachableTracker{}
		decide := func(node string, now time.Time) accessibleDecision {
			in := baseInput(tracker)
			in.node, in.now, in.fetchFrontend = node, now, failing
			d, _ := decideCsiAccessible(&in)
			return d
		}
		for i, n := range names {
			decide(n, epoch.Add(time.Duration(i)*20*time.Second))
		}
		// every node keeps being evaluated while its streak ages, then crosses unknownTimeout in turn
		for tick := 0; tick <= 20; tick++ {
			now := epoch.Add(time.Duration(tick) * 20 * time.Second)
			for i, n := range names {
				got := decide(n, now)
				if streak := now.Sub(epoch.Add(time.Duration(i) * 20 * time.Second)); streak >= 5*time.Minute && got != accessibleKeep {
					t.Fatalf("%s at streak %v: got %v, want keep", n, streak, got)
				}
			}
		}
	})
}

func TestUnreachableTrackerShareUsesTheWindow(t *testing.T) {
	tr := &unreachableTracker{}
	timeout := 5 * time.Minute
	tr.observe("c", "n1", epoch, true)
	tr.observe("c", "n2", epoch, true)
	// a slow sweep comes back to the nodes after more than unknownTimeout but within the window
	if unknown, total := tr.share("c", epoch.Add(8*time.Minute), 2*timeout); unknown != 2 || total != 2 {
		t.Fatalf("share = %d of %d, want 2 of 2", unknown, total)
	}
	if _, total := tr.share("c", epoch.Add(11*time.Minute), 2*timeout); total != 0 {
		t.Fatalf("total = %d, want 0 once past the window", total)
	}
}

func TestDecideCsiAccessibleSlowSweepsKeepEveryNodesStreak(t *testing.T) {
	tracker := &unreachableTracker{}
	decide := func(node string, now time.Time) accessibleDecision {
		in := baseInput(tracker)
		in.node, in.now = node, now
		in.unknownTimeout = 30 * time.Second
		in.fetchFrontend = func() frontendState { return frontendUnknown }
		d, _ := decideCsiAccessible(&in)
		return d
	}

	// two unknown nodes evaluated 1s apart, sweeps 65s apart (more than 2 x unknownTimeout)
	decide("a", epoch)
	decide("b", epoch.Add(time.Second))
	for _, sweep := range []time.Duration{65 * time.Second, 130 * time.Second} {
		for i, node := range []string{"a", "b"} {
			if got := decide(node, epoch.Add(sweep+time.Duration(i)*time.Second)); got != accessibleRemove {
				t.Fatalf("%s at sweep +%v: got %v, want remove", node, sweep, got)
			}
		}
	}
}

func TestDecideCsiAccessibleTrackerEntries(t *testing.T) {
	tracker := &unreachableTracker{}
	in := baseInput(tracker)
	in.stats = healthyStats(epoch)
	if got, _ := decideCsiAccessible(&in); got != accessibleSet {
		t.Fatalf("got %v, want set", got)
	}
	if unknown, total := tracker.share("ns/client", epoch, time.Minute); unknown != 0 || total != 1 {
		t.Fatalf("a healthy node must be tracked as healthy: %d of %d", unknown, total)
	}

	in.nodeReady = false
	if got, _ := decideCsiAccessible(&in); got != accessibleRemove {
		t.Fatalf("got %v, want remove", got)
	}
	if _, total := tracker.share("ns/client", epoch, time.Minute); total != 0 {
		t.Fatalf("a reliably decided node must be dropped from the tracker, total = %d", total)
	}
}
