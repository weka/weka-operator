# Envoy Management Proxy

`funcs_management_proxy.go` (ConfigMap + Deployment + Service + Ingress) and
`funcs_management_service.go` (endpoint selection). One proxy per WekaCluster, fronting up to
`MaxManagementServiceEndpoints` (10) backends.

## Config & rollout

Rendered config is split in two, both in one ConfigMap (mounted at `/etc/envoy`, one volume, no
second mount):

- `envoy.yaml` (bootstrap) — everything except endpoint IPs. `weka_backend` (const
  `wekaBackendClusterName`) is `type: EDS` with `eds_cluster_config.path_config_source` pointing at
  `eds.yaml` plus a `watched_directory` on `/etc/envoy`, so Envoy reloads it live.
- `eds.yaml` — a filesystem `DiscoveryResponse` (`ClusterLoadAssignment` for `weka_backend`) holding
  `lb_endpoints`. `version_info` is `util.GetHash` of the endpoint list — informational to Envoy,
  useful for diffing.

Only the bootstrap's hash goes on the pod template (`EnvoyConfigHashAnnotation`): Envoy never
reloads that file, so only a bootstrap change (ports, health-check tuning) rolls the pods. Pure
endpoint churn only rewrites `eds.yaml`, applied live. `generateEnvoyConfig` renders both;
`ensureManagementConfigMap` writes both keys. `selectActiveContainersForManagement` sorts its result
by name as a final step so reordering the same set doesn't touch `eds.yaml`'s bytes.

Rendering **fails** rather than emitting an endpoint-less cluster (Envoy would accept it while
resetting every connection). A bad `eds.yaml` write is otherwise silent — Envoy's watcher rejects it
and keeps the previous endpoints — so render/write errors here must propagate and log, never be
swallowed.

Tunables come from `MANAGEMENT_PROXY_*` env, gathered into `managementProxySettings`. Defaults live
in `internal/config/env.go`, not the chart: `manager.yaml` omits each var when unset so there is one
place a default is written. `adminBindAddress` defaults to loopback under `hostNetwork` (admin is
unauthenticated, includes `/quitquitquit`). All are operator-global — no per-WekaCluster override —
so changing one re-rolls every managed cluster's proxy.

## Labels & rollout

Selector labels are frozen separately from pod labels (`Spec.Selector` is immutable). Under
`hostNetwork`, `updateStrategy` uses `MaxUnavailable: 1, MaxSurge: 0`: a surge pod can't start while
the outgoing pod still holds the node's ports, and nothing steers a surge pod to a different node,
so it would just crash-loop on the same ports. Replace-in-place instead, with two-or-more replicas
keeping one ready throughout. Known gap, predating the field-exposure work: under `hostNetwork`
nothing stops two replicas (or two clusters' proxies, which share admin port 9901) from landing on
one node and colliding on host ports — `containerPort` without `hostPort` is invisible to the
scheduler. Fixing it needs a per-cluster admin port plus scoped anti-affinity, or `hostPort`.

## Endpoint selection (`selectActiveContainersForManagement`)

Drive/compute containers on the cluster's base port that pass `discovery.IsContainerOperational`,
Running ones first, capped at `MaxManagementServiceEndpoints`.

Candidates are iterated in name order, not `r.containers`' cache-List order, and the result is
re-sorted by name before returning. The two-pass selection emits Running containers first, so a
container changing state while staying operational would otherwise move within the slice: same set,
different `eds.yaml` bytes, a ConfigMap write and an Envoy reload that changes no membership.

Known gap: `IsContainerOperational` also rejects the transient statuses (`PodNotRunning`,
`Starting`) and a non-READY `InternalStatus`, so a restart/upgrade flap does change the endpoint set
and rolls the proxy. Making membership depend only on durable facts is a separate change.

## healthyPanicThreshold and proxy start

Defaults to 50 -- Envoy's own default, and what installs ran before the field was configurable, so
exposing it changed no behaviour. It fails open: a false negative from the single
`/api/v2/healthcheck` endpoint degrades to round-robin across unchecked hosts rather than leaving
zero selectable hosts and an unreachable management plane. The value is always emitted, so a
present-but-zero is 0% -- omitting the field would silently mean 50.

Setting 0 always honours health checks, so a partly reachable backend set degrades instead of
round-robining dead endpoints. That makes proxy *start* the risk -- Envoy holds every host unhealthy
until its first check -- so a starting replica is kept out of the Service rather than failing open.
The gating is unconditional, so lowering the threshold needs no other change:

- `/ready` gates it wherever `adminProbeHost` can reach admin, which is every default. Measured: 200
  once the first check round *resolves*, even against a backend that stays `failed_active_hc` — it
  waits for the round, not for a healthy host.
- `readinessInitialDelaySeconds` waits out interval+timeout where the probe degrades to TCP.
- `minReadySeconds` backs that up for rollout accounting; without it a rollout replaces the second
  replica before the first can serve.

## Probing a loopback admin bind

`adminProbeHost` picks kubelet's target. A wildcard bind answers on the pod IP (kubelet's default;
`::` needs `ipv4_compat`, which is why `adminIsIPv6Wildcard` exists). A loopback bind does not — but
under `hostNetwork` the pod runs in the node's network namespace, the one kubelet dials from, so
`HTTPGetAction.Host` set to the bind address reaches it. That keeps `/ready` on the `hostNetwork`
default instead of degrading to TCP, which cannot catch an Envoy wedged with its listener still
bound — a liveness gap, since the TCP probe never restarts it.

The target is the node's loopback, not the pod's, so it only distinguishes replicas because
`hostNetwork` already admits one per node (see the host-port gap above): a second binds 9901 and
fails rather than answering for the first.

TCP remains for the one combination left unreachable, loopback admin on the pod network, which
nothing defaults to — it needs an explicit `adminBindAddress` with `hostNetwork: false`. An `exec`
probe would cover that too, at the cost of depending on `curl` in `envoyImage`, which is
user-overridable.
