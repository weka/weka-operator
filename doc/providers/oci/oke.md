# OCI OKE

The operator runs on Oracle Kubernetes Engine (OKE) with default Helm values. The one OCI-specific requirement is data NICs for WEKA pods.

## What the operator does on OCI

- **Provider detection:** a node whose `spec.providerID` starts with `ocid1.` is treated as OCI (`internal/services/discovery/discovery.go`).
- **Data NICs:** on OCI nodes, WEKA pods that join the cluster request `weka.io/weka-nics` (one per core). Explicit devices, `network.udpMode: true` or `allocateVfPerIoNode: false` turn this off. See [Network Configuration](../../operator/networking/network-configuration.md).
- **Node removal:** OCI counts as a managed cloud. `podTerminationDeactivationTimeout` is 30m unless set explicitly (elsewhere the default is 0, never deactivate), and with `cleanupRemovedNodes: auto` the grace period before a removed node's containers are deleted is 30m instead of 24h.
- **No scale-down hook:** the termination lifecycle hook is AWS-only (see [AWS EKS](../aws/eks.md)).

## Pre-requisite: data NICs (ensure-nics)

Before creating the WekaCluster, attach data VNICs with `ensure-nics` and `type: oci`, or the WEKA pods that join the cluster stay `Pending` with `Insufficient weka.io/weka-nics`:

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaManualOperation
metadata:
  name: ensure-nics-backends
  namespace: default
spec:
  action: ensure-nics
  image: quay.io/weka.io/weka-in-container:<weka-version>
  imagePullSecret: quay-io-robot-secret
  payload:
    ensureNICsPayload:
      type: oci
      dataNICsNumber: 4
      nodeSelector:
        weka.io/supports-backends: "true"
```

`dataNICsNumber` doesn't count the primary VNIC and must fit the shape's VNIC limit. `4` works on `VM.DenseIO.E4.Flex`. For sizing, the periodic `WekaPolicy` form, verification and troubleshooting, see [Ensure NICs](../../operator/operations/ensure-nics.md).

The instance principal (or credentials) running on the nodes must be allowed to attach VNICs. The exact IAM policy isn't documented yet.

To skip data NICs, set `network.udpMode: true` on the WekaCluster. This lowers performance and suits functional testing only.

## Node pool

- **Shape:** use a DenseIO shape for backends, so the nodes have local NVMe drives. Verified with `VM.DenseIO.E4.Flex`.
- **Node image:** Oracle Linux 8.10 with UEK 5.15 is verified and needs no special handling.
- **Hugepages and other node settings:** the same as any cluster; see [Pre-requisites](../../operator/deployment/pre-requisites.md).
- **Labels:** label the nodes for their roles, e.g. `weka.io/supports-backends=true`, and match `nodeSelector` in the WekaCluster and in `ensure-nics`.

## Verifying

```bash
kubectl get nodes -o custom-columns='NAME:.metadata.name,NICS:.status.allocatable.weka\.io/weka-nics'
```

Every node that will run WEKA pods (backends, and clients if they join this cluster) should show the number of data NICs before you create the WekaCluster. Then follow [Cluster Provisioning](../../operator/deployment/cluster-provisioning.md).

## Related

- [Ensure NICs](../../operator/operations/ensure-nics.md)
- [AWS EKS](../aws/eks.md)
- [Helm install](../../operator/deployment/helm-install.md)
