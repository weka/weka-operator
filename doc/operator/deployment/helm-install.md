Installed with command as
```bash
# Install CRDs using server-side apply
helm show crds oci://quay.io/weka.io/helm/weka-operator --version v1.5.0 | \
  kubectl apply --server-side -f -

# Install operator
helm upgrade --create-namespace \
    --install weka-operator oci://quay.io/weka.io/helm/weka-operator \
    --namespace weka-operator-system \
    --version v1.5.0 --values operator_values.yaml
```
Always make sure to update CRDs before upgrading helm using server-side apply to handle large CRD definitions.

Operator pod can be found with label `app=weka-operator` after installation

notable helm-level values:

`driveSharing.driveTypesRatio` - Global default ratio for drive types (TLC vs QLC) when using drive sharing (default: `tlc: 1, qlc: 10`)

`localDataPvc` - global PVC to use for container's local data
Helm-level configure of PVC is deprecated and should be used only when explicitly instructed
If just asked to "use pvc for cluster", use cluster spec field instead. `globalPVC` object field on cluster spec with, with `name` field on it, which references PVC

`portAllocation.startingPort` - Starting port for Weka container port allocation (default: `35000`). This is the base port from which the operator allocates port ranges for Weka clusters. Only modify if you have specific firewall or port conflict requirements.

`cleanupRemovedNodes` - Controls what happens to a backend WekaContainer when its target node leaves the K8s cluster (default: `"auto"`). `"false"` never deletes the container; `"true"` deletes it immediately; `"auto"` holds the container in `Stale` status for a grace period (30m on managed cloud AWS/OCI, 24h otherwise) and deletes it only if the node is still gone at the end, so a transient node loss (reboot, ASG churn, brief API-server blip) no longer forces an unnecessary teardown. Quote the value. **Note:** the default changed from the previous effective `"false"` (never delete) to `"auto"` — deployments that do not set this value explicitly will now delete removed-node backends after the grace period.
