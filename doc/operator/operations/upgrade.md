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
Each S3 container has one off-path sidecar: `envoy` on older Weka versions, `forwarder` on versions whose image has the `s3_off_path_forwarder` feature flag. When the target image has the flag and envoy containers still exist, the sidecar is replaced node by node behind the S3 roll, in the order the Weka upgrade uses:

1. S3 on the node rolls to the target image. The new S3 waits for the load-balancer port, which the old envoy still holds.
2. Once the S3 pod runs the target image (not once it is ready, which would need the port):
   - A forwarder is created on that node with the target image, pinned to it. Forwarders are not created on nodes whose S3 still runs the old image.
   - The operator enables the forwarder era for the S3 (`weka debug jrpc s3_fwd_set_era hostId=<id> on=true`, idempotent, repeated until the envoy is gone); without it the new S3 never binds the LB port.
3. The envoy on that node is retired. Each reconcile reads where it stands from the envoy itself (`/server_info`, its ingress gauges, and `ss -ltnp` on the LB port), the forwarder and the S3, so nothing is recorded, on the objects or in the operator. While envoy listens on the LB port, these gates are checked in order:
   - Fast drain: envoy must run the immediate drain policy with a drain time of at most 5s, and its current process must be up at least 35s (the hot-restarted parent exits after 30s). While it reports another policy, the operator runs an embedded copy of the Weka build's `envoy_hotrestart.sh` (idempotent) with `weka local exec -C envoy` in the envoy pod, at most every 15s. Bounded at 90s into the retirement, as in the Weka upgrade; after that the drain is non-graceful. Events: `EnvoyFastDrainReady` / `EnvoyFastDrainTimedOut`.
   - Forwarder: its `/ready` on the admin socket (`/opt/weka/forwarder/admin.sock`) must report idle, healthy and probing S3. Event: `ForwarderReadyBesideEnvoy`.
   - Handover: `wekas3` must listen on the LB port beside the envoy (checked with `ss -ltnp` in the S3 container), so the endpoint is not dark during the handover. Bounded at 210s into the retirement (the fast-drain bound plus 120s), also when the check itself keeps failing; after that it drains anyway. Events: `EnvoyHandoverConfirmed` / `EnvoyHandoverTimedOut`.
4. The envoy is drained: `POST /drain_listeners?graceful` when the immediate policy is in effect, otherwise `POST /drain_listeners`, on the admin port with `curl` inside the envoy pod. It is sent on every pass while envoy still listens; a repeated request does not restart a running graceful drain. The operator waits until `http.ingress_http.downstream_rq_active` and `downstream_cx_active` both read 0 and envoy no longer listens on the LB port, or until 240s into the retirement (the handover bound plus 30s, the Weka upgrade's `--s3-drain-timeout` default), then deletes the envoy WekaContainer (`EnvoyRetired`). The forwarder binds the admin port by itself once the envoy is gone, and the next S3 does not roll until the forwarder is applied.

The retirement's time is read from the cluster on every pass, not stored: it starts at the latest start of the S3 container on the target image, the forwarder container, and the envoy process (its `uptime_all_epochs`). A restarted envoy runs the stock drain policy and listens again, and a forwarder still being created does not use up the later steps' time. An operator restart or leader failover therefore does not reset the timeouts. While the S3 or forwarder container is missing or not running, no timeout fires, so the envoy keeps serving until its replacement runs. An envoy that cannot be read is waited for, and deleted without a drain once the 240s are up and the forwarder is ready. The time also runs while the forwarder era cannot be set (the S3 has no cluster container id yet, or `s3_fwd_set_era` keeps failing). If that lasts past 240s, the first pass after the era is set skips the fast drain and the handover wait, sends a non-graceful drain and deletes the envoy once the forwarder is ready.

With `upgradeAllAtOnce`, envoys are not rolled to the target image. Each is deleted without a drain once the S3 on its node runs the target image, right after the forwarder era is set for that S3.

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
