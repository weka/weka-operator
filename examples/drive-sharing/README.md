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
| `02-cluster-simple-capacity.yaml` | containerCapacity with global driveTypesRatio | Uses Helm default ratio (tlc:1, qlc:10) |
| `03-cluster-fixed-drive-capacity.yaml` | driveCapacity + numDrives (TLC only) | Simple TLC-only, uniform drive sizes |
| `04-cluster-mixed-drive-types.yaml` | containerCapacity with cluster-specific driveTypesRatio | Override global ratio per cluster |
| `05-multiple-clusters-shared-nodes.yaml` | Production + Dev clusters sharing drives | Multi-tenant deployments |

**Note:** Examples 02 and 04 both use `containerCapacity` mode. The difference is:
- **02** relies on the global `driveTypesRatio` from Helm values
- **04** explicitly sets `driveTypesRatio` to override the global default for this cluster

## Configuration Modes

### Mode 1: Fixed Drive Capacity (TLC Only)

Set capacity per individual virtual drive. **Only TLC drives are used**:

```yaml
dynamicTemplate:
  numDrives: 6
  driveCapacity: 1000  # 1TB per TLC drive = 6TB total
```

### Mode 2: Container Capacity with Drive Types Ratio (Recommended)

Set total capacity per container with control over TLC/QLC distribution:

```yaml
dynamicTemplate:
  containerCapacity: 12000
  driveTypesRatio:
    tlc: 4  # 80% TLC
    qlc: 1  # 20% QLC
```

**TLC-only with containerCapacity:** Set `qlc: 0`:

```yaml
dynamicTemplate:
  containerCapacity: 8000
  driveTypesRatio:
    tlc: 1  # 100% TLC
    qlc: 0  # No QLC
```

## Global Default: driveTypesRatio

Set a global default TLC/QLC ratio in Helm values:

```yaml
# values.yaml
driveSharing:
  driveTypesRatio:
    tlc: 4  # 80% TLC
    qlc: 1  # 20% QLC
```

When `containerCapacity` is set without per-cluster `driveTypesRatio`, this global default applies automatically.
Default is 1:10 TLC/QLC ratio (tlc: 1, qlc: 10).

## Complete Documentation

For detailed information, see:
- [Drive Sharing Guide](../../doc/operator/operations/drive-sharing.md)
- [WekaCluster API Reference](../../doc/api_dump/wekacluster.md)
- [WekaManualOperation API Reference](../../doc/api_dump/wekamanualoperation.md)