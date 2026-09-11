# Weka Home CR Reporter

**Path**: `internal/reporter/`

Periodically snapshots operator-managed objects (5 weka CRs, operator Deployment,
DaemonSets, Pods, Node projection) to Weka Home as gzipped kind-tagged NDJSON
(`POST /api/v4/operator/deployments/{id}/snapshot`). Enabled via
`wekahome.reporter.enabled` (default on); identity = keypair+GUID Secret, RS256 SRT JWT.

| File | Purpose |
|------|---------|
| `reporter.go` | Report loop, registration latch, `buildSnapshot` |
| `collector.go` | CR-kind registry + Deployment/DaemonSet/Pod collectors |
| `collector_nodes.go` | Node projection (weka.io-scoped labels/annotations) |
| `collector_events.go` | Events List (uncached reader) + per-object `_events` index |
| `serializer.go` | NDJSON envelope, strip, `_events` graft |
| `identity.go` | Deployment identity + registration |
| `transport.go` | TLS/proxy-aware HTTP client, gzipped send |

Each object's JSON embeds its kubectl-describe Events section as a synthetic
top-level `_events` array (all types, describe-like projection, sorted by last-seen).

See [services index](index.md) for related service entry points.
