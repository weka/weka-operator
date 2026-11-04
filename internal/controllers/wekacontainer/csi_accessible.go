package wekacontainer

import (
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/kubectl/pkg/util/podutils"

	"github.com/weka/weka-operator/internal/config"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

var (
	csiUnreachableNodes = &unreachableTracker{}
)

// minSystemicNodes is the fewest nodes tracked within the window for which a shared failure can be called systemic;
// below it one dead node would count as the whole client.
const minSystemicNodes = 3

// runStatusUnknown is WEKA's run status when it cannot read its own state.
const runStatusUnknown weka.ContainerStatus = "Unknown"

type accessibleDecision int

const (
	accessibleRemove accessibleDecision = iota
	accessibleSet
	// accessibleKeep leaves the current label as it is and never adds it.
	accessibleKeep
)

func (d accessibleDecision) String() string {
	return [...]string{"remove", "set", "keep"}[d]
}

type csiPodState int

const (
	csiPodUnknown csiPodState = iota
	csiPodHealthy
	csiPodUnhealthy
)

// nodeReadySince returns when the node's Ready condition last turned true, or the zero time when unknown.
func nodeReadySince(node *v1.Node) time.Time {
	for _, c := range node.Status.Conditions {
		if c.Type == v1.NodeReady && c.Status == v1.ConditionTrue {
			return c.LastTransitionTime.Time
		}
	}
	return time.Time{}
}

// csiContainerNotServing reports whether WEKA itself says the client container cannot serve: its internal status,
// written only from a real `weka local ps` reply, is not READY, or (flag permitting) its run status is not Running.
func csiContainerNotServing(c *weka.WekaContainer) bool {
	if c.Status.InternalStatus != "" && c.Status.InternalStatus != "READY" {
		return true
	}
	if !config.Config.Csi.PreventNewWorkloadOnClientContainerNotRunning {
		return false
	}
	switch c.Status.Status {
	case weka.Running:
		return false
	// Error is written by the operator (setErrorStatus) when a step fails, e.g. `weka local ps` could not be run;
	// WEKA's run status is a fixed enum that never contains it. Unknown means WEKA cannot read its own state.
	case weka.Error, runStatusUnknown:
		return false
	}
	return true
}

// csiAccessibleInput is what one label evaluation knows about a client container.
type csiAccessibleInput struct {
	now       time.Time
	nodeReady bool
	// nodeBootID is the node's current boot id; stats sampled under another boot do not describe this one.
	nodeBootID string
	// readySince is when the node last became Ready; stats from before it describe a previous boot or outage.
	readySince         time.Time
	driversBootIDStale bool
	// clientPodGone: this container's own pod is missing, terminating or not Running.
	clientPodGone  bool
	csiPod         csiPodState
	wekaNotServing bool
	stats          *weka.WekaContainerMetrics

	statsFreshness           time.Duration
	unknownTimeout           time.Duration
	systemicFailureThreshold int

	// fetchFrontend asks the node-agent whether the driver reports every frontend connected.
	fetchFrontend func() frontendState

	tracker *unreachableTracker
	// trackerKey identifies the WekaClient, or the container itself when it has none.
	trackerKey string
	node       string
}

// decideCsiAccessible evaluates the signals in order of reliability; the first match decides. The order and the
// settings are in the table in doc/operator/deployment/embedded-csi.md.
func decideCsiAccessible(in *csiAccessibleInput) (decision accessibleDecision, reason string) {
	decision, reason = decideReliable(in)
	if decision != accessibleKeep {
		return decision, reason
	}
	return decideUnknown(in)
}

// decideReliable covers the signals that decide on their own: boot id, node readiness, CSI pod, `weka local ps`,
// fresh stats and the node-agent. It answers accessibleKeep when the frontend state could not be read.
func decideReliable(in *csiAccessibleInput) (decision accessibleDecision, reason string) {
	switch {
	case in.driversBootIDStale:
		reason = "node booted since the drivers were loaded"
	case !in.nodeReady:
		reason = "node is not ready"
	case in.clientPodGone:
		reason = "client pod is missing, terminating or not running"
	case in.csiPod == csiPodUnhealthy:
		reason = "csi node pod is missing or not ready"
	case in.wekaNotServing:
		reason = "weka reports the container is not running or not READY"
	}
	if reason != "" {
		in.tracker.clear(in.trackerKey, in.node)
		return accessibleRemove, reason
	}

	if in.stats != nil && in.stats.BootID != "" && in.stats.BootID == in.nodeBootID &&
		in.stats.LastUpdate.After(in.readySince) && in.now.Sub(in.stats.LastUpdate.Time) < in.statsFreshness {
		s := in.stats
		processesKnown := s.Processes.Created > 0 && s.ProcessesPulledAt.After(in.readySince) &&
			in.now.Sub(s.ProcessesPulledAt.Time) < in.statsFreshness
		driverKnown := s.DriverFrontends.Created > 0
		if (processesKnown && s.Processes.Active < s.Processes.Created) ||
			(driverKnown && s.DriverFrontends.Active < s.DriverFrontends.Created) {
			in.tracker.clear(in.trackerKey, in.node)
			return accessibleRemove, "stats report processes or driver frontends not connected"
		}
		if processesKnown && driverKnown {
			in.tracker.observe(in.trackerKey, in.node, in.now, false)
			return accessibleSet, "stats report all healthy"
		}
	}

	switch in.fetchFrontend() {
	case frontendConnected:
		in.tracker.observe(in.trackerKey, in.node, in.now, false)
		return accessibleSet, "node-agent reports the driver frontend connected"
	case frontendNotConnected:
		in.tracker.clear(in.trackerKey, in.node)
		return accessibleRemove, "node-agent reports the driver frontend not connected"
	}
	return accessibleKeep, "frontend state unknown"
}

// decideUnknown handles an unreadable frontend state: keep the label for unknownTimeout, then keep it only while
// the failure is systemic across the WekaClient's nodes. See the table in doc/operator/deployment/embedded-csi.md.
func decideUnknown(in *csiAccessibleInput) (decision accessibleDecision, reason string) {
	first := in.tracker.observe(in.trackerKey, in.node, in.now, true)
	if in.now.Sub(first) < in.unknownTimeout {
		return accessibleKeep, "frontend state unknown, within unknown timeout"
	}
	// a peer stays counted for twice unknownTimeout: a sweep over a large client can take that long to return to it
	unknown, total := in.tracker.share(in.trackerKey, in.now, 2*in.unknownTimeout)
	if total >= minSystemicNodes && unknown*100 >= in.systemicFailureThreshold*total {
		return accessibleKeep, "frontend state unknown on most nodes of the client, assuming operator, network or API failure"
	}
	return accessibleRemove, "frontend state unknown past the unknown timeout"
}

// unreachableTracker remembers, per WekaClient and node, the latest evaluation (healthy or unknown) and since when
// the frontend state has been unknown. It is in memory: an operator restart only delays a removal.
type unreachableTracker struct {
	mu      sync.Mutex
	clients map[string]map[string]*nodeProbe
}

type nodeProbe struct {
	firstUnknown time.Time // zero while the latest evaluation was healthy
	lastSeen     time.Time
}

// observe records one evaluation that left the node's label as it is or set it, and returns when the node's
// current unknown streak began (zero if healthy). The streak start is kept until a healthy evaluation, a reliable
// verdict (clear) or the container's cleanup ends it, however far apart evaluations are.
func (t *unreachableTracker) observe(client, node string, now time.Time, unknown bool) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.clients == nil {
		t.clients = map[string]map[string]*nodeProbe{}
	}
	nodes := t.clients[client]
	if nodes == nil {
		nodes = map[string]*nodeProbe{}
		t.clients[client] = nodes
	}
	p := nodes[node]
	if p == nil {
		p = &nodeProbe{}
		nodes[node] = p
	}
	p.lastSeen = now
	switch {
	case !unknown:
		p.firstUnknown = time.Time{}
	case p.firstUnknown.IsZero():
		p.firstUnknown = now
	}
	return p.firstUnknown
}

// clear drops a node a reliable signal has decided (or whose container is gone): it is neither healthy nor unknown.
func (t *unreachableTracker) clear(client, node string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.clients[client], node)
	if len(t.clients[client]) == 0 {
		delete(t.clients, client)
	}
}

// share counts the client's nodes evaluated within window: how many are unknown, and how many in all.
func (t *unreachableTracker) share(client string, now time.Time, window time.Duration) (unknown, total int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, p := range t.clients[client] {
		if now.Sub(p.lastSeen) > window {
			continue
		}
		total++
		if !p.firstUnknown.IsZero() {
			unknown++
		}
	}
	return unknown, total
}

// csiPodStateOf maps the CSI node DaemonSet pods listed for a node to a state: no pod, no Ready pod, or only
// terminating ones is unhealthy.
func csiPodStateOf(pods []v1.Pod) csiPodState {
	for i := range pods {
		if pods[i].DeletionTimestamp == nil && podutils.IsPodReady(&pods[i]) {
			return csiPodHealthy
		}
	}
	return csiPodUnhealthy
}
