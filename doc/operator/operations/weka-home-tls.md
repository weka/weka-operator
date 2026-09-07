# Weka Home TLS

## Overview

Weka Home is Weka's telemetry and support-connectivity service. It is **not
on the data path** — it carries events, connectivity heartbeats, and
(optionally) performance stats, never IO. A misconfigured CA certificate
degrades support visibility; it does not affect mounts, IO, or cluster
availability.

This document explains how Weka Home connectivity works, how its CA
certificate is configured across the WekaCluster, WekaClient, and operator
surfaces, and the constraint that makes this different from most other
per-object settings: the CA **path** is cluster-wide replicated
configuration, but the CA **file** is not distributed anywhere.

For the Kubernetes Secrets the operator manages for cluster authentication
(distinct from the Weka Home CA secrets described here), see
[secrets-management.md](secrets-management.md).

## How Weka Home connectivity works

- **Every Weka machine talks to Weka Home directly.** Backends and clients
  each open their own HTTPS connection to the configured endpoint (default
  `api.home.weka.io:443`). A client does not hand its telemetry to a backend
  to forward under normal conditions.
- **A backend relay is a fallback only.** A client relays through a
  cluster backend's management HTTP port only after its own direct attempt
  fails — DNS failure, connection refused, timeout, or TLS handshake
  failure — and only when the cluster permits the relay. If Weka Home is
  reachable but rejects the upload (e.g. an application-level error), there
  is no relay for that: relaying would not fix a rejection.
- **Blocking clients from the internet is supported but has consequences.**
  Each client burns a failed direct attempt, caches "unreachable" for about
  30 minutes, relays through a backend during that window, then re-probes
  directly. The cluster raises an event both on entering and on leaving this
  state, so it's observable. Relay is unavailable in the cluster's strictest
  TLS mode, and was off by default before Weka 6.1 — in those cases, client
  telemetry is simply lost while direct connectivity is blocked. For
  air-gapped sites, use the supported disable-call-home cluster setting
  instead of a firewall rule, so the cluster stops retrying and alerting on
  something that's expected to fail.
- **The gRPC trace channel is separate from all of this.** It's disabled by
  default, takes its endpoint from cluster configuration, excludes client
  containers unless explicitly opted in, and **does not verify the server
  certificate at all** — it consults neither a custom CA nor the OS trust
  store. Nothing in this document applies to it.

## The CA path is cluster-wide; the file is not

> **This is the single most important fact about Weka Home TLS
> configuration.** `weka_cloud_ca_cert_path` is cluster-wide replicated
> configuration — every machine that joins the cluster receives the full
> config snapshot, including this path and the Weka Home base URL. But
> **only the path string replicates. Nothing distributes the certificate
> file itself.** Every machine that joins must place a PEM at that path on
> its own.

Two consequences follow directly from this:

- When the path is **unset**, a machine passes no explicit CA to its Weka
  Home connection and falls back to its container's **OS trust store** — a
  local trust anchor that its own owner controls. This is what makes a
  public Weka Home endpoint (a public CA) work with zero configuration on
  every machine, managed or not.
- When the path **is set**, an explicit CA **replaces** the system bundle
  rather than adding to it — so the OS-trust-store fallback stops working
  for every machine on that cluster, not just the one that set the path.

This means setting `wekaCluster.spec.wekaHome.cacertSecret` imposes a
filesystem-path contract on **every machine that joins that cluster**,
including machines this operator does not manage: bare-metal hosts, clients
in other Kubernetes clusters, or clients run by other teams. Each of them
must independently place a PEM at
`/opt/weka/k8s-runtime/vars/wh-cacert/cert.pem` — a path that is decidedly
odd to create by hand on a bare-metal host. Weigh that blast radius before
setting the field; the operator does not warn about it, because using a
private CA cluster-wide is a supported configuration, not a mistake.

## The three configuration surfaces

| Surface | Field | Effect |
|---|---|---|
| WekaCluster | `spec.wekaHome.cacertSecret` | Mounts the Secret into every backend pod, stages it at `/opt/weka/k8s-runtime/vars/wh-cacert/cert.pem`, and sets the cluster-wide `weka_cloud_ca_cert_path` from a drive container. |
| WekaClient | `spec.wekaHome.cacertSecret` | Places the same file on the client pod at the same path. When `targetCluster` is set and the cluster is in the **same namespace**, this is **derived automatically from the target cluster's own `cacertSecret`** — set it explicitly only to override that default. A cluster in another namespace is not inherited from: only the Secret *name* would be copied, and it would not resolve in the client's namespace, so the client emits a warning event and you must set the field yourself. |
| Operator (Helm) | `wekahome.cacertSecret` | Used only by the operator's own CR reporter (the process that periodically reports CRs to Weka Home). It reads the Secret through the Kubernetes API into an in-memory certificate pool at request time. **No volume, no mount, no container filesystem is involved.** |

Every PEM in the Secret's data is concatenated into the destination file or
pool — the data-key name does not matter, for either the WekaCluster/
WekaClient mount path or the operator's in-memory pool. Secrets keyed
`cert.pem` work unchanged.

The operator's own reporter starts from the system cert pool and adds the
Secret's certificates on top of it, so it never loses the OS trust store the
way the container-side path does.

Clients run in Weka's `--restricted` mode and can never set cluster
configuration themselves — they only need the certificate file present at
the expected path; the cluster's own configuration is what tells them (and
everything else) which endpoint and CA to use.

### Secret shape and example

```bash
kubectl create secret generic weka-home-ca \
  --from-file=cert.pem=./private-weka-home-ca.pem
```

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: my-cluster
spec:
  wekaHome:
    cacertSecret: weka-home-ca
```

Because any key name works, `--from-file=ca.crt=...` or a multi-key Secret
with several PEMs under different keys is equally valid — all of them end up
concatenated into the one trust bundle on disk.

## Client-only deployments (no `targetCluster`)

When a WekaClient connects to an external backend via `spec.joinIpPorts`
instead of `spec.targetCluster`, the operator has no visibility into that
external cluster and cannot see or verify its `weka_cloud_ca_cert_path`.

Setting `spec.wekaHome.cacertSecret` on such a client places a PEM at the
*operator's own* path
(`/opt/weka/k8s-runtime/vars/wh-cacert/cert.pem`) — which only helps if the
external cluster's administrator happens to have set the same path
convention. The operator cannot confirm this, so it emits an admission
warning (`client_wekahome_cacert_unverifiable`) on any WekaClient that sets
`wekaHome.cacertSecret` while leaving `targetCluster` unset.

**The supported answer for this topology is the container's OS trust
store**, per Weka's own recommendation for machines outside the cluster's
management scope: bake the CA into the client image, or mount a CA bundle
over `/etc/ssl/certs` or `/etc/pki` in the container. This works precisely
when the external cluster leaves its own `weka_cloud_ca_cert_path` unset, so
every machine — managed or not — falls back to its own OS trust store.

`spec.extraVolumes`/`spec.extraVolumeMounts` on the WekaClient is the
declarative way to do that without a custom image — see
[extra-volumes.md](../deployment/extra-volumes.md), which carries a worked
example for exactly this topology. Baking the CA into a custom client image
also works.

## Rotation

Rotating the CA Secret's content requires a pod restart on both paths:

- The operator's in-memory cert pool is built once when the reporter's HTTP
  client is constructed, so the operator pod must restart to pick up a
  changed Secret.
- The container-side file at
  `/opt/weka/k8s-runtime/vars/wh-cacert/cert.pem` is copied in at container
  boot, so backend and client pods need to restart as well.

Updating the Secret's contents in place does not propagate to either side
until the corresponding pod restarts.

Changing the Secret *name* (`spec.wekaHome.cacertSecret` on a WekaCluster or
WekaClient) is propagated to the owned WekaContainer specs right away, but the
running pods are not recreated automatically either: delete them and the
replacements mount the new Secret. Pods created after the change use it
without any action.
