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
