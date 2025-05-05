# Cluster Provisioning Guide

## Overview
This document details the process of provisioning a Weka cluster in Kubernetes using the Weka Operator. It covers basic configuration, networking options, and advanced settings for optimizing cluster performance and resilience.

## Basic Cluster Provisioning
Below is an example for a Weka cluster provision. Use this as a starting point for testing and development:

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: demo-provision
  namespace: NAMESPACE
spec:
  template: dynamic
  gracefulDestroyDuration: 0s
  dynamicTemplate:
    computeContainers: 10
    driveContainers: 10
    computeCores: 2
    driveCores: 2
    computeHugepages: 10000
    numDrives: 2
  overrides: {}
  image: quay.io/weka.io/weka-in-container:4.4.2.157-k8s.2
  nodeSelector:
    weka.io/dedicated: "dedicated-group"
  driversDistService: "https://weka-drivers-dist.weka-operator-system.svc.cluster.local:60002"
  imagePullSecret: "quay-io-robot-secret"
```

Each type of container is singleton within a node, and minimal cluster size is 5 compute and 5 drive containers.

## Graceful Termination
- `spec.gracefulDestroyDuration` controls the graceful termination period
- Default is 24h (if not specified)
- Setting to 0s allows for quicker cluster deletion during testing/development
- When a cluster is deleted with graceful termination > 0, all containers will move to "paused" status
- When timeout expires, the cluster will fully destroy all WekaContainers

## Optional Features and Parameters

### S3 Support
- `spec.dynamicTemplate.s3Containers` - Set to provision cluster with S3 support

### Hot Spare Capacity
- `spec.hotSpare` - Number of FDs (Failure Domains) to be used as hot spare capacity
- Default is 0

### Raft Configuration
For clusters with more than 10 drive/compute containers:
```yaml
spec:
  leadershipRaftSize: 9
  bucketRaftSize: 9
```

### Redundancy Settings
For clusters with more than 20 drive containers:
```yaml
spec:
  redundancyLevel: 4
  stripeWidth: 16
```
- Default: redundancyLevel=2, stripeWidth=auto-calculated
- These settings ensure largest stripe width on erasure coding/RAID level and enable +4 protection from failures

### I/O Conditions
```yaml
spec:
  startIoConditions:
    minNumDrives: 20
```
- `minNumDrives` should be ~80% of driveContainers * numDrives
- Recommended for clusters with >10 drive containers and >2 drives per container

## Cluster Status Monitoring
After provisioning, monitor the cluster status through the Kubernetes API:
- `status.status` field should reach "Ready" state
- If not "Ready" within 10 minutes, consider the provisioning failed