# Status-Only Resource Allocation

## Summary

Removed node annotations from the resource allocation flow. Allocations are now stored **only** in `WekaContainer.Status.Allocations`.

## What Changed

### Before (Hybrid Approach)
```
1. List containers → Parse node annotations + container Status
2. Allocate resources
3. Update node annotations (claims)
4. Update WekaContainer Status
5. On deletion: Finalizer cleans node annotations
```

### After (Status-Only)
```
1. List containers → Read allocations from container Status
2. Allocate resources
3. Update WekaContainer Status (single source of truth)
4. On deletion: No cleanup needed (Status deleted automatically)
```

## Benefits

✅ **50% fewer API calls** - No node updates.  
✅ **No annotation size limits** - Status objects can grow larger (etcd default ~1.5MB).  
✅ **Simpler architecture** - Single source of truth.  
✅ **Faster deletion** - No cleanup step.  
✅ **Self-documenting** - `kubectl get wekacontainer -o yaml` shows all allocations.  

## Leader Election for Conflict Prevention

To prevent race conditions when multiple containers allocate on the same node, we use **per-node leader election** with Kubernetes Leases.

### How It Works

```
All containers on node-1 (any namespace) compete for:
  Lease: "weka-operator-system/weka-allocator-node-1"

Container A (default/backend-0):
  1. Requests lease ← Acquires it
  2. Reads all container Status on node-1
  3. Allocates available drives/ports
  4. Updates own Status
  5. Releases lease

Container B (other-ns/backend-1):
  1. Requests lease ← Waits (A holds it)
  2. ... waits ~2-5 seconds ...
  3. A releases → B acquires lease
  4. Reads all container Status (includes A's allocation)
  5. Allocates remaining resources
  6. Updates own Status
  7. Releases lease
```

### Key Design Decisions

#### 1. Cluster-Wide Per-Node Leases

**Lease Scope:**
- Name: `weka-allocator-{nodeName}`
- Namespace: `weka-operator-system` (cluster-wide)
- Identity: `{containerNamespace}/{containerName}`

**Why cluster-wide?**
- Drives and ports are **node-level resources**, not namespace-level
- If `namespace-a/container-1` claims `/dev/sda`, then `namespace-b/container-2` **cannot** use it
- Per-namespace leases would allow conflicts between namespaces

#### 2. Automatic Lease Expiration

```go
LeaseDuration: 15 seconds   // Lease expires if not renewed
RenewDeadline: 10 seconds   // Holder must renew within this time
RetryPeriod:   2 seconds    // Non-leaders retry every 2s
```

**Crash Recovery:**
```
Container A:
  1. Acquires lease
  2. Allocates resources
  3. 💥 Crashes (pod killed, node failure, etc.)
  4. Stops renewing lease
  5. Lease expires after 15 seconds

Container B:
  1. Waiting...
  2. 15 seconds later → lease available
  3. Acquires expired lease
  4. Reads Container A's Status (if it was written)
  5. Allocates remaining resources ✅
```

### Resource Exhaustion Backoff

To prevent starving other containers when resources are insufficient:

```go
// In doAllocateResourcesWithLease:
if err := allocateResources(); err != nil {
    if errors.As(err, &InsufficientDrivesError{}) {
        // Wait 30s before retrying (not 5s)
        // Gives other containers a chance to acquire lease
        return lifecycle.NewWaitErrorWithDuration(err, 30*time.Second)
    }
}
```

**Without backoff:**
```
Container A: Acquire lease → Insufficient drives → Retry in 5s → Acquire again → Still insufficient
Container B: Waiting forever (A keeps getting lease first)
```

**With backoff:**
```
Container A: Acquire lease → Insufficient drives → Wait 30s
Container B: Acquires lease → Also insufficient → Wait 30s
Container C: Gets a chance... (fair distribution)
```

### Lease Permissions (RBAC)

Added to operator ServiceAccount:
```yaml
- apiGroups: ["coordination.k8s.io"]
  resources: ["leases"]
  verbs: ["create", "delete", "get", "list", "patch", "update", "watch"]
```

Files updated:
- `charts/weka-operator/templates/role.yaml`
- `internal/controllers/wekacontainer/controller.go` (kubebuilder marker)

Run `make manifests` to regenerate RBAC from markers.

## Migration

### From Hybrid to Status-Only

Existing deployments with node annotations:
1. Old annotations remain but are **ignored**
2. New allocations use Status only
3. Gradual migration as containers are recreated

## Testing

### Verify Status-Only Allocation

```bash
# Check container allocations
kubectl get wekacontainer <name> -o jsonpath='{.status.allocations}' | jq

# Verify no node annotations are created
kubectl get node <node-name> -o jsonpath='{.metadata.annotations}' | grep -i weka
```

### Expected Behavior

- ✅ `WekaContainer.Status.Allocations` populated with drives, ports
- ✅ Node annotations empty or contain only old data
- ✅ Container deletion does not update node
