# Drive Sharing Examples

This directory contains example configurations for drive sharing, which enables multiple Weka clusters to share physical NVMe drives on Kubernetes nodes.

## Quick Start

1. **Sign drives for proxy mode** (required once):
   ```bash
   kubectl apply -f 01-sign-drives-for-proxy.yaml
   ```

2. **Create cluster with drive sharing**:
   ```bash
   # Simple approach (recommended)
   kubectl apply -f 02-cluster-simple-capacity.yaml

   # OR fixed capacity per drive
   kubectl apply -f 03-cluster-fixed-drive-capacity.yaml

   # OR mixed drive types (TLC/QLC)
   kubectl apply -f 04-cluster-mixed-drive-types.yaml
   ```

3. **Verify**:
   ```bash
   # Check proxy containers
   kubectl get pods -n weka-operator-system -l weka.io/mode=ssdproxy

   # Check cluster status
   kubectl get wekacluster -n default
   ```

## Files

| File | Description | Use Case |
|------|-------------|----------|
| `01-sign-drives-for-proxy.yaml` | Sign drives for proxy mode | **Required first step** - enables drive sharing |
| `02-cluster-simple-capacity.yaml` | Cluster with total container capacity | **Recommended** - flexible, operator-optimized |
| `03-cluster-fixed-drive-capacity.yaml` | Cluster with fixed capacity per drive | Simple, predictable, uniform drive sizes |
| `04-cluster-mixed-drive-types.yaml` | Cluster with TLC/QLC drive ratio | Performance/cost balance with mixed drives |
| `05-multiple-clusters-shared-nodes.yaml` | Production + Dev clusters sharing drives | Multi-tenant deployments |

## Configuration Modes

### Mode 1: Simple Container Capacity (Recommended)

Set total capacity per container, operator distributes optimally:

```yaml
dynamicTemplate:
  containerCapacity: 8000  # 8TB per container
```

### Mode 2: Fixed Drive Capacity

Set capacity per individual virtual drive:

```yaml
dynamicTemplate:
  numDrives: 6
  driveCapacity: 1000  # 1TB per drive = 6TB total
```

### Mode 3: Mixed Drive Types

Split capacity between TLC and QLC drives:

```yaml
dynamicTemplate:
  containerCapacity: 12000
  driveTypesRatio:
    tlc: 4  # 80% TLC
    qlc: 1  # 20% QLC
```

## Global Default: driveTypesRatio

Set a global default TLC/QLC ratio in Helm values:

```yaml
# values.yaml
driveTypesRatio:
  tlc: 4  # 80% TLC
  qlc: 1  # 20% QLC
```

When `containerCapacity` is set without per-cluster `driveTypesRatio`, this global default applies automatically.
Default is TLC-only (tlc: 1, qlc: 0).

## Complete Documentation

For detailed information, see:
- [Drive Sharing Guide](../../doc/operator/operations/drive-sharing.md)
- [WekaCluster API Reference](../../doc/api_dump/wekacluster.md)
- [WekaManualOperation API Reference](../../doc/api_dump/wekamanualoperation.md)