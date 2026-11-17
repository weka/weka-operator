# Cluster Upgrade Guide

## Overview
This document details the process for upgrading Weka clusters and clients in Kubernetes environments. It covers the upgrade workflow, monitoring techniques, and best practices to ensure successful upgrades.

## Upgrading Weka Cluster

### Upgrade Process
- Upgrade is initiated by changing the `image` field in the WekaCluster CR
- The operator will rotate all pods in the cluster by updating the image field on WekaContainers
- Containers are upgraded one by one with verification between each step

When upgrading weka backend containers, rolling upgrade strategy is used. It upgrades drive containers first, then compute containers, and finally the frontend and protocol containers. This staged process allows parts of the system to continue serving clients while others are updated.
Once the backend servers have been upgraded, client components are upgraded separately, by updating WekaClient CR. 

### Upgrading from envoy to forwarder (S3)
Each S3 container has one off-path sidecar: `envoy` on older Weka versions, `forwarder` on versions whose image has the `s3_off_path_forwarder` feature flag. When the target image has the flag and envoy containers still exist, the sidecar is replaced node by node behind the S3 roll:

1. S3 on the node rolls to the target image. The new S3 waits for the load-balancer port, which the old envoy still holds.
2. Once the S3 pod runs the target image (not once it is ready, which would need the port), the envoy on that node is retired:
   - The operator enables the forwarder era for the S3 (`weka debug jrpc s3_fwd_set_era hostId=<id> on=true`, idempotent, repeated until the envoy is gone); without it the new S3 never binds the LB port.
   - It hot-restarts the envoy with the immediate drain policy (an embedded copy of the Weka build's `envoy_hotrestart.sh`, run with `weka local exec -C envoy` in the envoy pod), then waits for `GET /server_info` to show `Immediate` with a drain time of at most 5s and for the parent envoy to exit (35s). The wait is bounded at 90s, as in the Weka upgrade; after that the drain below is non-graceful. Events: `EnvoyFastDrainReady` / `EnvoyFastDrainTimedOut`.
   - It marks the envoy so a forwarder is created on that node with the target image, while the envoy still holds the LB admin port, and waits until the forwarder's `/ready` on its admin socket (`/opt/weka/forwarder/admin.sock`) reports it idle, healthy and probing S3, as the Weka upgrade does before it drains envoy. Event: `ForwarderReadyBesideEnvoy`.
   - It waits until `wekas3` listens on the LB port beside the envoy (checked with `ss -ltnp` in the S3 container) so the endpoint is not dark during the handover. The wait is bounded at 120s; after that it drains anyway. An `EnvoyHandoverConfirmed` or `EnvoyHandoverTimedOut` event records which happened.
   - It drains the envoy (`POST /drain_listeners?graceful` when the immediate policy is in effect, otherwise `POST /drain_listeners`, on the admin port, run with `curl` inside the envoy pod). The drain start is recorded in the `weka.io/internal-envoy-drain-started` annotation, which the spec sync preserves.
   - It waits until `http.ingress_http.downstream_rq_active` and `downstream_cx_active` both read 0 and envoy no longer listens on the LB port (a graceful drain keeps accepting connections for its drain time, and a client that reconnects then would be reset), or until 30s pass (the Weka upgrade's `--s3-drain-timeout` default).
   - It deletes the envoy WekaContainer.
3. Once the envoy is gone the forwarder binds the admin port by itself. Forwarders are not created while envoys exist, except on a node whose envoy the upgrade has reached, and are pinned to that node. The next S3 does not roll until the forwarder is applied.

With `upgradeAllAtOnce`, all envoys are deleted without a drain.

### Monitoring Upgrade Progress
An indication of a finished upgrade is when the `status.lastAppliedImage` field is updated to match the latest version. You can monitor the progress with:

```bash
kubectl get wekacontainer -n NAMESPACE -l weka.io/cluster-name=CLUSTER_NAME -o custom-columns=NAME:.metadata.name,WEKA_SIDE_CONTAINER_NAME:.spec.name,IMAGE:.spec.image
```

Weka status monitoring:
The `upgrade_info.upgrade_phase` field in the `weka status --json` output provides insight into which part of the upgrade process is currently in progress (for example, differentiating between the drive, compute, or frontend upgrade phases).
After the upgrade, `weka status` confirms that the cluster is running the new version.

### Upgrade Failure Detection
If no pods are recreated within 10 minutes or if no changes are observed for 10 minutes, consider the upgrade failed.

## Upgrading Weka Clients

### Client Upgrade Process
- Upgrade is initiated by changing the `image` field in the WekaClient CR
- The upgrade process will update the `wekaImage` on all WekaContainers that belong to the WekaClient
- All client containers are upgraded at once
- Each WekaContainer will replace its pod with the latest image when there are no active I/Os

### Upgrade with Workload Considerations
For proper testing of upgrades with workloads:
1. Create WekaCluster and WekaClient with toleration (`rawToleration`) to all taints with key `weka.io/upgrade`
2. Change the WekaClient image to initiate upgrade
3. Apply the taint on all matching nodes to evict workloads
4. Validate that workloads are evicted and pods are replaced with the new image

## Best Practices
- Always monitor the upgrade process closely
- Consider setting up monitoring or automation to detect stalled upgrades
- Plan upgrades during maintenance windows or low-usage periods
- Ensure cluster health before initiating upgrades
- Have a rollback plan in case the upgrade fails
