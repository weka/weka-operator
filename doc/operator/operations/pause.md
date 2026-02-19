# Cluster Pause

## Overview

The cluster-level `paused` override allows gracefully stopping all containers in a Weka cluster without deleting the cluster. Containers are stopped and their pods removed, but the cluster resource and all configuration are preserved.

## Usage

Set the `paused` field in the cluster's `spec.overrides`:

```yaml
apiVersion: weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: my-cluster
spec:
  overrides:
    paused: true
```

To unpause, explicitly set it to `false`:

```yaml
spec:
  overrides:
    paused: false
```

To remove cluster-level control and allow direct container-level manipulation, remove the field entirely (or set to `null`).

## Three-State Behavior

The `paused` field is a nullable boolean (`*bool`) with three distinct states:

| Value | Behavior |
|---|---|
| not set (`nil`) | No propagation. The cluster does not enforce pause state. Direct container-level `spec.state: paused` manipulation is respected and won't be overridden. Removing the field after pausing does **not** unpause — use `false` to actively recover. |
| `true` | All containers are gracefully stopped (S3 first, then NFS, then the rest). Cluster status becomes `Paused`. Normal reconciliation is suspended. |
| `false` | Containers that are in `paused` state are moved to `active`. Containers in other states (e.g. `deleting`, `destroying`) are not affected. Normal reconciliation resumes. |

## Interaction with Cluster Deletion

The `paused` flag does **not** block cluster deletion. If a cluster is marked for deletion, the normal deletion flow proceeds regardless of the `paused` value. Use `cancelDeletion` to prevent destruction.

| `paused` | Deletion state | Behavior |
|---|---|---|
| `nil` | no deletion | No pause propagation |
| `true` | no deletion | Containers paused, cluster status = "Paused" |
| `false` | no deletion | Paused containers recovered to active |
| any | deleted | Normal deletion flow (grace period then destruction) |
| `nil` | deleted + `cancelDeletion` | Deletion cancelled, paused containers recovered |
| `true` | deleted + `cancelDeletion` | Deletion cancelled, containers **stay paused** |
| `false` | deleted + `cancelDeletion` | Deletion cancelled, paused containers recovered |

## Propagation to WekaClients

When a `WekaClient` references a cluster via `spec.targetCluster`, the cluster's `paused` override propagates to client containers automatically. The WekaClient controller checks the target cluster's pause state on each reconcile and applies the same three-state logic to its own containers:

| Cluster `paused` | Effect on client containers |
|---|---|
| `nil` | No propagation — client containers are unaffected |
| `true` | All client containers are paused |
| `false` | Paused client containers are recovered to active |

This means pausing a cluster pauses both its own backend/S3/NFS containers and all dependent client containers. Unpausing recovers both.

Client container propagation happens during the WekaClient reconcile cycle (no dedicated watch on WekaCluster), so there may be a short delay before client containers react to a cluster pause state change.

WekaClients without `targetCluster` set are never affected by cluster pause.

Since the WekaClient controller does not watch WekaCluster resources directly, propagation happens on the client's next reconcile cycle (periodic resync). Cluster containers are paused immediately; client containers follow shortly after.

## Prerequisites

Before setting `paused: true`, stop IO on the Weka cluster:

```bash
weka cluster stop-io
```

Stopping IO first ensures data safety and a clean shutdown. The operator does not manage this step automatically, as there are pause scenarios where stopping IO may not be possible or desired.

> **Note:** Future versions will add configurability for automated stop-io, force stop-io, and bypass options.

## Pause Flow Details

When `paused: true` is set, the operator:

1. Sets cluster status to `Paused`
2. Patches S3 containers to `spec.state: paused`
3. Patches NFS containers to `spec.state: paused`
4. Patches all remaining containers to `spec.state: paused`
5. Each container controller stops its weka process, removes the pod, and reports `status: Paused`
6. Reconciliation finishes early — no further cluster-level steps run

## Unpause Flow Details

When `paused` is changed from `true` to `false`, the operator:

1. Finds containers with `spec.state: paused` (only those — other states are untouched)
2. Patches them to `spec.state: active`
3. Each container controller starts the normal active flow (creates pod, starts weka, etc.)
4. Normal cluster reconciliation resumes
