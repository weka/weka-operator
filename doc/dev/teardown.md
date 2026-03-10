# Weka Cluster Teardown

For AI agents. All commands must complete within 30s — use timeouts, poll with `kubectl get` between steps.

```bash
# Safe delete pattern
kubectl delete <kind> -n <namespace> <name> --timeout=30s
# If still present, poll and retry:
kubectl get <kind> -n <namespace> <name>
```

---

## Step 1 — Delete Workloads Using Weka Storage

Skip if no Weka-backed PVCs exist.

```bash
# Discover
kubectl get storageclass | grep weka
kubectl get pvc -A
kubectl get pods -A -o json | \
  jq -r '.items[] | select(.spec.volumes[]?.persistentVolumeClaim.claimName != null) |
  "\(.metadata.namespace) \(.metadata.name) \(.spec.volumes[].persistentVolumeClaim.claimName // empty)"'

# Delete workload owner then PVCs
kubectl delete <kind> -n <namespace> <name> --timeout=30s
kubectl delete pvc -n <namespace> <pvc-name> --timeout=30s

# Poll until gone
kubectl get pvc -n <namespace>
```

---

## Step 2 — Confirm Zero Mounts on Client Containers

```bash
# Poll until MOUNTS is 0 or <none> for all client containers
kubectl get wekacontainer -A -o custom-columns='NS:.metadata.namespace,NAME:.metadata.name,MOUNTS:.status.mountCount'
```

---

## Step 3 — Delete WekaClient

```bash
kubectl delete wekaclient -n <namespace> <name> --timeout=30s

# Poll until no client WekaContainers remain
kubectl get wekacontainer -n <namespace>
```

---

## Step 4 — Delete WekaCluster

```bash
kubectl delete wekacluster -n <namespace> <name> --timeout=30s

# Poll until gone; operator removes drive/compute containers, CSI DaemonSet, StorageClasses
kubectl get wekacluster,wekacontainer -n <namespace>
```
