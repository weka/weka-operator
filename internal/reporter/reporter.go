package reporter

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
)

// CRReporter periodically collects all Weka CRs cluster-wide and POSTs a full
// combined snapshot to Weka Home.
type CRReporter struct {
	client client.Client
	// apiReader is the uncached reader used for the per-cycle events List —
	// the cached client would informer-cache all cluster events forever.
	apiReader  client.Reader
	wh         config.WekaHome
	namespace  string
	identity   *IdentityManager
	log        logr.Logger
	httpClient *http.Client

	// registered latches true once Weka Home registration succeeds (201/409).
	registered bool
}

// New constructs a CRReporter. namespace is the operator namespace.
func New(c client.Client, apiReader client.Reader, wh config.WekaHome, namespace string, identity *IdentityManager, log logr.Logger) *CRReporter {
	return &CRReporter{
		client:    c,
		apiReader: apiReader,
		wh:        wh,
		namespace: namespace,
		identity:  identity,
		log:       log.WithName("reporter"),
	}
}

const defaultInterval = time.Minute

// Run starts the reporting loop. It blocks until ctx is cancelled.
func (r *CRReporter) Run(ctx context.Context) {
	if r.wh.Endpoint == "" {
		// No endpoint ⇒ nothing to report to; disable rather than POST to a
		// host-less URL every cycle.
		r.log.Info("Weka Home endpoint not configured — reporter disabled")
		return
	}

	interval := r.wh.Reporter.Interval
	if interval <= 0 {
		r.log.Info("Reporter interval not set or invalid; using default", "default", defaultInterval)
		interval = defaultInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	r.log.Info("Starting Weka Home reporter", "interval", interval, "endpoint", r.wh.Endpoint)

	// Report immediately so a fresh deployment is visible without waiting a full
	// interval; reportOnce self-guards, so an early failure just retries next tick.
	r.reportOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			r.log.Info("Stopping Weka Home reporter")
			return
		case <-ticker.C:
			r.reportOnce(ctx)
		}
	}
}

// reportOnce runs a single all-or-nothing reporting cycle: build the combined
// snapshot, resolve the deployment GUID (for the ingest path), then send it. If
// building fails, nothing is sent (prevents partial snapshots that would
// soft-delete the missing kind on the Weka Home side).
func (r *CRReporter) reportOnce(ctx context.Context) {
	// Build the TLS-aware HTTP client lazily, retrying each cycle, so a late cacert
	// Secret (or a transient read failure) recovers without a restart. The default
	// client would silently ignore AllowInsecure/CacertSecret, so skip until built.
	// Once built it's reused for the process lifetime: the cacert pool is captured at
	// build time, so a rotated CacertSecret is only picked up on restart (rotation is rare).
	if r.httpClient == nil {
		hc, err := buildHTTPClient(ctx, r.client, r.wh, r.namespace, sendTimeout)
		if err != nil {
			r.log.Error(err, "Failed to build Weka Home HTTP client; retrying next cycle (skipping this snapshot)")
			return
		}
		r.httpClient = hc
	}

	// Register before sending, retrying each cycle (not only at startup) so a Weka
	// Home outage at boot self-heals later. Until registered, Weka Home has no
	// public key for us and would reject the snapshot, so skip the send.
	if !r.registered {
		if err := r.identity.Register(ctx, r.httpClient); err != nil {
			r.log.Error(err, "Weka Home registration not complete; retrying next cycle (skipping this snapshot)")
			return
		}
		r.registered = true
		r.log.Info("Weka Home registration complete")
	}

	snapshot, err := r.buildSnapshot(ctx)
	if err != nil {
		return // already logged in buildSnapshot
	}

	depID, err := r.identity.DeploymentID(ctx)
	if err != nil {
		r.log.Error(err, "Failed to resolve deployment ID — skipping send")
		return
	}

	resp, err := send(ctx, r.httpClient, r.wh.Endpoint, depID, snapshot, r.identity)
	if err != nil {
		r.log.Error(err, "Failed to send combined snapshot")
		return
	}
	if resp != nil {
		r.log.Info("Snapshot ingested",
			"objects_processed", resp.ObjectsProcessed,
			"objects_marked_deleted", resp.ObjectsMarkedDeleted,
			"cluster_links", resp.ClusterLinks)
	}
}

// buildSnapshot collects every reported kind into one kind-tagged NDJSON buffer
// and returns the raw (un-gzipped) bytes. If ANY collection/serialization step
// fails it returns an error and the caller must not send.
func (r *CRReporter) buildSnapshot(ctx context.Context) ([]byte, error) {
	var buf bytes.Buffer
	totalObjects := 0

	// Accumulate union of node-selectors from CR kinds that expose them.
	var nodeSelectors []map[string]string

	// Best-effort: a failed events List logs and skips enrichment this cycle.
	evIdx := collectEventIndex(ctx, r.apiReader, r.log)

	// --- CR kinds ---
	for _, kind := range crKinds {
		rawList, err := collectListRaw(ctx, r.client, kind)
		if err != nil {
			r.log.Error(err, "Failed to collect CRs — aborting cycle", "kind", kind.name)
			return nil, err
		}

		objs := kind.items(rawList)

		if err := appendNDJSON(&buf, kind.name, objs, evIdx); err != nil {
			r.log.Error(err, "Failed to serialize CRs — aborting cycle", "kind", kind.name)
			return nil, err
		}
		totalObjects += len(objs)

		if kind.selectors != nil {
			nodeSelectors = append(nodeSelectors, kind.selectors(rawList)...)
		}
	}

	// --- Operator Deployment ---
	deployments, err := collectDeployment(ctx, r.client, r.namespace)
	if err != nil {
		r.log.Error(err, "Failed to collect operator Deployment — aborting cycle")
		return nil, err
	}
	if err := appendNDJSON(&buf, "Deployment", deployments, evIdx); err != nil {
		r.log.Error(err, "Failed to serialize operator Deployment — aborting cycle")
		return nil, err
	}
	totalObjects += len(deployments)

	// --- Operator-owned DaemonSets (node-agent + embedded CSI node) ---
	daemonsets, err := collectDaemonSets(ctx, r.client)
	if err != nil {
		r.log.Error(err, "Failed to collect operator DaemonSets — aborting cycle")
		return nil, err
	}
	if err := appendNDJSON(&buf, "DaemonSet", daemonsets, evIdx); err != nil {
		r.log.Error(err, "Failed to serialize operator DaemonSets — aborting cycle")
		return nil, err
	}
	totalObjects += len(daemonsets)

	// --- Operator-owned Pods ---
	pods, err := collectPods(ctx, r.client)
	if err != nil {
		r.log.Error(err, "Failed to collect operator Pods — aborting cycle")
		return nil, err
	}
	if err := appendNDJSON(&buf, "Pod", pods, evIdx); err != nil {
		r.log.Error(err, "Failed to serialize operator Pods — aborting cycle")
		return nil, err
	}
	totalObjects += len(pods)

	// --- Node projection ---
	nodeSummaries, err := collectNodes(ctx, r.client, nodeSelectors)
	if err != nil {
		r.log.Error(err, "Failed to collect Node projection — aborting cycle")
		return nil, err
	}
	for i := range nodeSummaries {
		nodeSummaries[i].Events = evIdx.forNode(nodeSummaries[i].Name)
	}

	if err := appendNodeNDJSON(&buf, nodeSummaries); err != nil {
		r.log.Error(err, "Failed to serialize Node projection — aborting cycle")
		return nil, err
	}
	totalObjects += len(nodeSummaries)

	r.log.V(1).Info("Built combined snapshot", "total_objects", totalObjects, "nodes", len(nodeSummaries), "bytes", buf.Len())
	return buf.Bytes(), nil
}
