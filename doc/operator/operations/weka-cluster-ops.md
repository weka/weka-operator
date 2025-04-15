# Weka Cluster Operations Guide

## Overview
This document provides information about operating and managing Weka clusters in Kubernetes. It covers cluster status monitoring, command execution, and container identification across Weka and Kubernetes environments.

## Cluster Status Monitoring

### Example of `weka status` Output
```
       cluster: cluster-name (550c68b6-8f64-4073-8086-efc3ba69207e)
        status: OK (17/21 backend containers UP, 36 drives UP)
    protection: 3+2 (Fully protected)
     hot spare: 0 failure domains
 drive storage: 210.59 TiB total, 209.00 TiB unprovisioned
         cloud: connected
       license: Unlicensed

     io status: STARTED 19 days ago (29/33 io-nodes UP, 156 Buckets UP)
    link layer: Ethernet
       clients: 4 connected
         reads: 0 B/s (0 IO/s)
        writes: 0 B/s (0 IO/s)
    operations: 6 ops/s
        alerts: 52 active alerts, use `weka alerts` to list them
```

### Cluster Health Status
The following cluster statuses indicate a healthy system:
- `OK`
- `REDISTRIBUTING`
- `PARTIALLY_PROTECTED`
- `REBUILDING`

### JSON Output Option
For programmatic use, the status command supports JSON output:
```bash
weka status --json
```

## Commonly Used Commands

### Finding Leadership Processes
To find leadership processes and associated container names:
```bash
weka cluster processes -o id,container -l
```

### Finding the Current Leader
To identify the current leader among the leadership processes:
```bash
weka cluster processes -o id,container -L
```

## Correlating Weka Containers to Kubernetes Resources

### Container Name Mapping
Weka container names can be correlated to Kubernetes container names using the WekaContainer's `spec.name` field.

### Finding Weka Containers related to a specific cluster
```bash
kubectl get -n NAMESPACE wekacontainer -o custom-columns=NAME:.metadata.name,SPEC_NAME:.spec.name -l weka.io/cluster-name=CLUSTER_NAME,weka.io/mode=drive
```

Sample output:
```
NAME                                                                          SPEC_NAME
test-raft9resilience-4pblijjlbfj-drive-07d2d65f-2d75-4791-a737-779ac317a17a   drivex07d2d65fx2d75x4791xa737x779ac317a17a
test-raft9resilience-4pblijjlbfj-drive-0dbc4b5e-115a-433d-81bd-e4fabbd43298   drivex0dbc4b5ex115ax433dx81bdxe4fabbd43298
```

### Advanced Filtering
For easier parsing and command execution, use `--no-headers` with `grep` and `awk`:
```bash
kubectl get -n NAMESPACE wekacontainer -o custom-columns=NAME:.metadata.name,SPEC_NAME:.spec.name -l weka.io/cluster-name=CLUSTER_NAME,weka.io/mode=drive --no-headers | grep -e WEKA_CONTAINER_NAME | awk '{print $1}'
```

## WekaContainer Status Overview
Example of `kubectl get wekacontainer` output:
```
NAME                                                      STATUS    MODE      MANAGEMENT IPS   NODE      PROCESSES   DRIVES   MOUNTS   CPU    AGE    WEKA CID   MESSAGE
weka-infra-clients-h12-1-a                                Running   client    10.100.7.13                2/2/2                0        0.14   37h    111
weka-infra-compute-fdc1e475-e6fc-46b2-a601-7353bce76be4   Running   compute   10.100.7.13      h12-1-a   0/3/3                         0.18   5d8h   102
weka-infra-drive-fa5408a4-6f0a-4726-bcb5-d1cf75842c72     Running   drive     10.100.5.41      h1-1-a    3/3/3       6/6/6             0.02   5d8h   101
```