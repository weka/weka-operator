# Cluster CRD

## Overview

This CRD represents a Weka cluster backend.
Instantiating this CRD will reserve the specified number of cores and disks from a pool of resources.

## Example

```yaml
apiVersion: weka.io/v1alpha1
kind: Cluster
metadata:
  name: cluster-sample
spec:
  size_class: "small"
  multiplier: 1
```

## Configuration

| Field      | Description                                              | Type   | Required | Default |
| -----      | -----------                                              | ----   | -------- | ------- |
| size_class | A t-shirt style token indicating the size of the cluster | string | true     |         |
| multiplier | Scale the cluster by multiple `n`                        | int    | true     | 1       |

### Size Classes

| Size Class | Description |
| ---------- | ----------- |
| dev        |             |
| small      |             |
| medium     |             |
| large      |             |

## Behavior

The size class defines the number of containers and drives.
One node, with sufficient cores and drives must be allocated for each container.

To allocate a node, we apply the label `weka.io/reservation=<cluster-name>` to the node.
To indicate how many drives and cores are reservered, we further add the labels `weka.io.reservation.<cluster-name>.cores=<n>` and `weka.io.reservation.<cluster-name>.drives=<n>`.
Multiple clusters may be allocated to the same node if sufficient resources are available.
The available (unallocated) resources on that node are the total resources minus the sum of reserved resources.
