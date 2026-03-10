# Weka Data Catalog

The Data Catalog indexes filesystem metadata to enable fast search and data lifecycle management. It runs as a dedicated catalog cluster on top of the `data-services` containers.

## Prerequisites

- `dataServicesContainers: 2` (or more) in `WekaCluster.spec.dynamicTemplate`
- Both `DataServicesGlobalConfigured` and `CatalogClusterCreated` conditions must be `True`
- Operator automatically creates and joins all data-services containers

## Checking Status

```bash
# Quick cluster-level check via operator
kubectl get wekacluster -n default <cluster-name> -o jsonpath='{.status.conditions}' \
  | python3 -m json.tool | grep -A4 -i 'catalog\|dataserv'

# Get a data-services pod to run weka CLI
DS_POD=$(kubectl get pods -n default -l weka.io/cluster=<cluster-name>,weka.io/mode=data-services \
  --no-headers -o name | head -1)
# or just:
DS_POD=$(kubectl get pods -n default | grep data-services | grep -v fe | head -1 | awk '{print $1}')

# Catalog cluster nodes (coordinator + workers)
kubectl exec -n default $DS_POD -- weka catalog cluster status --json

# Catalog config (port, indexing interval, retention)
kubectl exec -n default $DS_POD -- weka catalog config show --json

# Per-filesystem indexing status
kubectl exec -n default $DS_POD -- weka catalog fs status --json

# Active indexing tasks
kubectl exec -n default $DS_POD -- weka catalog task --json
```

### Healthy output example

`weka catalog cluster status`:
```json
[
  { "role": "COORDINATOR", "state": "active", "hostName": "h1-4-c", "serviceName": "catalog-coordinator" },
  { "role": "WORKER",      "state": "active", "hostName": "h5-11-b", "serviceName": "catalog-worker-b" }
]
```

`weka catalog config show`:
```json
{
  "cluster": { "port": 14611, "coordinator_hostname": "h1-4-c", "jvm_heap_size_gb": 16 },
  "indexfs":  { "fs_name": ".indexfs" },
  "indexing": { "index_enabled": true, "index_interval": "86400s", "retention_period": "2592000s" }
}
```

## Configuration

The port is set automatically by the operator from `WekaCluster.status.ports.dataServicesPort`.

Index interval and retention period can be set via `WekaCluster.spec.catalog`:

```yaml
spec:
  catalog:
    indexInterval: "1d"     # how often to re-index (default 86400s = 1d)
    retentionPeriod: "30d"  # how long to keep indexed metadata (default 2592000s = 30d)
```

To update live without cluster recreation:

```bash
kubectl exec -n default $DS_POD -- \
  weka catalog config update --index-interval <INTERVAL> --retention-period <PERIOD>
```

## Enabling Indexing on a Filesystem

By default the `default` filesystem has `index_enabled: true` but no metadata ingested yet (`has_metadata: false`, `last_ingest_time: ""`). Ingest is triggered by the configured interval or manually:

```bash
# Check which filesystems are indexed
kubectl exec -n default $DS_POD -- weka catalog fs status --json

# Explore catalog subcommands
kubectl exec -n default $DS_POD -- weka catalog metadata --help
kubectl exec -n default $DS_POD -- weka catalog fs --help
```

## Architecture

| Component | K8s resource | Weka mode | Role |
|-----------|-------------|-----------|------|
| Catalog backend | `WekaContainer` mode=`data-services` | hosts catalog JVM process | coordinator or worker |
| Catalog frontend | `WekaContainer` mode=`data-services-fe` | proxies catalog API | required on same node as backend |
| Index filesystem | `.indexfs` Weka FS | internal | stores catalog index data |

Only `data-services` containers (not `-fe`) join the catalog cluster. The operator handles join/leave automatically during scale-up/down.

## Operator Conditions

| Condition | Meaning |
|-----------|--------|
| `DataServicesGlobalConfigured` | Global dataserv config applied to cluster |
| `CatalogClusterCreated` | `weka catalog cluster add .indexfs --containers <IDs>` succeeded with ≥2 data-services containers |
| `CatalogConfigured` | `weka catalog config update` applied (only if `spec.catalog` is set) |

## Troubleshooting

```bash
# Operator conditions stuck?
kubectl describe wekacluster -n default <cluster> | grep -A3 Catalog

# Container not joining?
kubectl logs -n default <data-services-pod> | grep -i catalog

# Check data-services-fe is up (required for catalog to function)
kubectl get pods -n default | grep data-services-fe
```
