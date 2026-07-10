package reporter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/pkg/util"
)

const registrationPath = "/api/v4/operator/deployments/register"

func ingestPathFor(deploymentID string) string {
	return "/api/v4/operator/deployments/" + deploymentID + "/snapshot"
}

// sendTimeout bounds a single combined snapshot upload. The shared util client
// defaults to 10s, too short for a large gzipped snapshot (a big cluster can be a
// few MB compressed).
const sendTimeout = 2 * time.Minute

// maxIngestResponseBytes bounds how much of the Weka Home response we buffer — a
// buggy/misrouted endpoint must not be able to OOM the operator with a huge body.
const maxIngestResponseBytes = 1 << 20 // 1 MiB

// maxErrorBodyBytes bounds the response-body snippet folded into a non-2xx error
// (Weka Home returns a helpful JSON/plain-text error body) — small enough for a log line.
const maxErrorBodyBytes = 4 << 10 // 4 KiB

// buildHTTPClient builds an HTTP client for Weka Home honoring the WekaHome TLS
// settings: AllowInsecure (skip verification) and CacertSecret (trust a
// private/on-prem CA). Every PEM value in the cacert Secret is added to the pool,
// so the data-key name does not matter.
func buildHTTPClient(ctx context.Context, c client.Client, wh *config.WekaHome, namespace string, timeout time.Duration) (*http.Client, error) {
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		baseTransport = &http.Transport{}
	}
	tr := baseTransport.Clone()

	if config.Config.Proxy != "" {
		proxyURL, err := url.Parse(config.Config.Proxy)
		if err != nil {
			return nil, fmt.Errorf("parse proxy %q: %w", config.Config.Proxy, err)
		}
		tr.Proxy = http.ProxyURL(proxyURL)
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if wh.AllowInsecure {
		// Don't read CacertSecret in insecure mode — a missing/empty one must not
		// break the client when the user explicitly opted into insecure TLS.
		tlsCfg.InsecureSkipVerify = true
	} else if wh.CacertSecret != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}

		secret := &corev1.Secret{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: wh.CacertSecret}, secret); err != nil {
			return nil, fmt.Errorf("read wekahome cacert secret %q in %q: %w", wh.CacertSecret, namespace, err)
		}

		added := false
		for _, pemBytes := range secret.Data {
			if pool.AppendCertsFromPEM(pemBytes) {
				added = true
			}
		}
		if !added {
			return nil, fmt.Errorf("wekahome cacert secret %q contains no valid PEM certificates", wh.CacertSecret)
		}
		tlsCfg.RootCAs = pool
	}

	tr.TLSClientConfig = tlsCfg
	return &http.Client{
		Transport: otelhttp.NewTransport(tr),
		Timeout:   timeout,
	}, nil
}

// ingestResponse is the Weka Home ingest response payload (the inner "data"),
// logged for observability.
type ingestResponse struct {
	DeploymentID         string        `json:"deployment_id"`
	ObjectsProcessed     int           `json:"objects_processed"`
	ObjectsMarkedDeleted int           `json:"objects_marked_deleted"`
	ClusterLinks         []clusterLink `json:"cluster_links"`
}

// clusterLink is one reported WekaCluster's link status — one entry per
// status.clusterID in the snapshot.
type clusterLink struct {
	Status    string `json:"status"` // linked | missing_cluster | not_reported
	ClusterID string `json:"cluster_id"`
}

type ingestEnvelope struct {
	Data ingestResponse `json:"data"`
}

// send POSTs the combined kind-tagged NDJSON body (gzipped) for a full snapshot
// cycle to the Weka Home ingest endpoint, with the deployment GUID as a path
// param. httpClient carries the TLS config (nil ⇒ util default); identity signs
// the SRT auth header. On 2xx it returns the parsed ingest response for logging;
// a 2xx with an empty/unparseable body is still success (returns nil, nil).
func send(ctx context.Context, httpClient *http.Client, endpoint, deploymentID string, ndjson []byte, identity *IdentityManager) (*ingestResponse, error) {
	authHeader, err := identity.AuthHeader(ctx)
	if err != nil {
		return nil, fmt.Errorf("get auth header: %w", err)
	}

	fullURL := strings.TrimRight(endpoint, "/") + ingestPathFor(deploymentID)
	resp, err := util.SendJsonRequest(ctx, fullURL, ndjson, util.RequestOptions{
		AuthHeader:  authHeader,
		GzipBody:    true,
		ContentType: "application/x-ndjson",
		Timeout:     sendTimeout,
		Client:      httpClient,
	})
	if err != nil {
		return nil, fmt.Errorf("send snapshot: %w", err)
	}
	defer func() {
		//nolint:errcheck // draining response body before close; error is not actionable
		_, _ = io.Copy(io.Discard, resp.Body)
		//nolint:errcheck // best-effort close on a drained response body
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= 300 {
		//nolint:errcheck // best-effort error snippet for a diagnostic message; a read failure just yields an empty snippet
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return nil, fmt.Errorf("send snapshot: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	// Best-effort parse for observability; bounded so a buggy endpoint can't OOM
	// us (the deferred drain streams any remainder for connection reuse).
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxIngestResponseBytes))
	if err != nil {
		return nil, nil
	}
	var env ingestEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, nil
	}
	return &env.Data, nil
}
