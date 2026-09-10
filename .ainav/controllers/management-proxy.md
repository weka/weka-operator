# Envoy Management Proxy

One proxy per WekaCluster fronts selected drive/compute management endpoints.
Source directory: `internal/controllers/wekacluster/`.

| File | Entry points |
|---|---|
| `funcs_management_proxy.go` | `generateEnvoyConfig`, `ensureManagementConfigMap`, `managementProxySettings`, `adminProbeHost` |
| `funcs_management_service.go` | `selectActiveContainersForManagement`, `MaxManagementServiceEndpoints` |
| `../../config/env.go` | `MANAGEMENT_PROXY_*` defaults |

## Invariants and caveats

- `envoy.yaml` is bootstrap configuration; only its hash is stored in the pod template's `EnvoyConfigHashAnnotation`. Bootstrap changes roll pods.
- `eds.yaml` contains endpoints and is watched live by Envoy. Endpoint-only changes update EDS without rolling pods; name sorting stabilizes unchanged membership.
- Selection uses operational drive/compute containers on the cluster base port, preferring Running containers. Transient status changes can still change membership.
- Rendering rejects an empty valid endpoint set. Propagate render/write errors; an invalid EDS update can leave Envoy serving previous endpoints.
- Under `hostNetwork`, replacement uses no surge. Placement does not prevent replicas or different clusters' proxies from colliding on host ports.
- `adminProbeHost` chooses HTTP admin probes when reachable; loopback admin on a pod network falls back to TCP. Readiness delay and minimum ready time allow the first health-check round to resolve.

Configuration, health-check behavior, probe routing and troubleshooting:
[management proxy guide](../../doc/operator/networking/management-proxy.md).
