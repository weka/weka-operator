# OKE VCN-Native Pod Networking and ensure-nics

## Overview

With OKE VCN-Native Pod Networking, OKE attaches pod VNICs to each node and lists them in the node's `NativePodNetwork` object (`oci.oraclecloud.com/v1beta1`, cluster-scoped, owned by the Node). The ensure-nics operation excludes these VNICs automatically, so they are never offered to Weka as data NICs.

## Behavior

- For each node, ensure-nics reads the MAC addresses in `status.vnics` of that node's `NativePodNetwork` and excludes them when selecting data NICs.
- The operator needs `get` and `list` on `nativepodnetworks` (group `oci.oraclecloud.com`). The Helm chart includes this rule.
- A node gets its ensure-nics container only after its `NativePodNetwork` reports `status.state: SUCCESS`. Until then the operation waits and retries; nodes that are ready are processed meanwhile. The wait has no timeout: if a node's `NativePodNetwork` stays `FAILED`, fix it on the OKE side or remove the node from the `nodeSelector`.
- On clusters without the `NativePodNetwork` CRD, or with no `NativePodNetwork` objects (for example OKE with Flannel), nothing is excluded and nothing waits.
- `dataNICsNumber` counts Weka data NICs only. Pod VNICs are not part of it.

## Shape limits

The shape's VNIC attachment limit must fit: 1 (primary VNIC) + pod VNICs + `dataNICsNumber`.

## Nodes where a pod VNIC was already offered

If an older operator already offered a pod VNIC as a data NIC on a node:

1. Re-run ensure-nics on the node. The `weka.io/weka-nics` annotation and the node's allocatable NIC resource are corrected.
2. Recreate any Weka container whose `weka.io/allocations` entry holds a pod-VNIC MAC. This is a manual step.

## Verification

Pod VNIC MACs per node:

```bash
kubectl get nativepodnetworks -o json \
  | jq -r '.items[] | .metadata.ownerReferences[0].name as $n | .status.vnics[]? | "\($n) \(.macAddress)"'
```

Data NICs the operator recorded for a node (none of the MACs above may appear here):

```bash
kubectl get node <node> -o jsonpath='{.metadata.annotations.weka\.io/weka-nics}'
```
