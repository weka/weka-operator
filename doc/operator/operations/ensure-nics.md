# Ensure NICs (cloud data NICs)

The `ensure-nics` operation attaches additional data NICs to cloud nodes (AWS, OCI) and publishes them to Kubernetes as the extended resource `weka.io/weka-nics`, so WEKA pods that join the cluster can be scheduled.

## When you need it

On a detected cloud provider (AWS, OCI), WEKA pods that join the cluster (drive, compute, client, S3, NFS, …) request `weka.io/weka-nics` (one per core). Without data NICs on the node they stay `Pending` with:

```
0/N nodes are available: N Insufficient weka.io/weka-nics
```

The request is not made, and `ensure-nics` is not needed, when the cluster or container uses `network.udpMode: true`, explicit devices (`ethDevice`, `ethDevices`, `deviceSubnets`, selectors) or `allocateVfPerIoNode: false`. See [Network Configuration](../networking/network-configuration.md).

## How it works

The operation runs `cloud-helper` inside an adhoc WEKA container on each selected node (pods named `weka-adhoc-<operation-or-policy-name>-<node-uid>`), attaches NICs through the cloud API, and records them in the node annotation `weka.io/weka-nics`. The operator then sets `weka.io/weka-nics` capacity and allocatable on the node status.

## One-shot: WekaManualOperation

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaManualOperation
metadata:
  name: ensure-nics-backends
  namespace: default
spec:
  action: ensure-nics
  image: quay.io/weka.io/weka-in-container:5.1.31
  imagePullSecret: quay-io-robot-secret
  payload:
    ensureNICsPayload:
      type: aws   # or oci
      dataNICsNumber: 3
      nodeSelector:
        weka.io/supports-backends: "true"
```

The example targets backend nodes only. `nodeSelector` must cover every node pool that runs WEKA pods joining the cluster, clients included; use one operation per pool if their labels differ.

## Periodic: WekaPolicy

Use a policy to cover nodes that join later. `interval` sets how often it re-runs.

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaPolicy
metadata:
  name: ensure-nics
  namespace: default
spec:
  type: ensure-nics
  image: quay.io/weka.io/weka-in-container:5.1.31
  imagePullSecret: quay-io-robot-secret
  payload:
    interval: 1h
    ensureNICsPayload:
      type: aws   # or oci
      dataNICsNumber: 3
      nodeSelector:
        weka.io/supports-backends: "true"
```

## Sizing `dataNICsNumber`

`dataNICsNumber` is the number of data NICs in addition to the primary NIC. The instance type's NIC limit applies to the total. Size it to cover the cores of all WEKA containers on the node.

Example: `i3en.3xlarge` allows 4 NICs in total, so `dataNICsNumber: 4` fails with:

```
MaxNicsExceededError: 5 NICs requested, but only 4 NICs are allowed for instance type i3en.3xlarge
```

## Credentials

- AWS: uses IRSA when the pod has a web-identity token (set `spec.serviceAccountName` on the WekaManualOperation or WekaPolicy to an IRSA-enabled service account); otherwise falls back to the node instance role through IMDS (log line `no IRSA web-identity in pod env, falling back to node instance role via IMDS`). EKS Pod Identity credentials are not forwarded into the nested container. The role needs EC2 network-interface permissions (create, attach, modify and describe network interfaces). To grant it through the node group role, attach the EC2 network-interface permissions listed above to the backend node group IAM role; the policy in [AWS EKS](../../providers/aws/eks.md) covers only Auto Scaling lifecycle hooks and does not include them.
- OCI: the IAM policy needed to attach VNICs is not documented yet.

## Verifying

The operation is complete when every selected node has the `weka.io/weka-nics` annotation and the manual operation reports `Done`:

```bash
kubectl get wekamanualoperation ensure-nics-backends -o jsonpath='{.status.status}'
kubectl get nodes -l weka.io/supports-backends=true \
  -o custom-columns='NODE:.metadata.name,NICS:.metadata.annotations.weka\.io/weka-nics'
kubectl get nodes -o custom-columns='NODE:.metadata.name,NICS:.status.allocatable.weka\.io/weka-nics'
```

## Troubleshooting

A failing operation does not move to a failed state: it retries. A WekaManualOperation keeps `.status` empty; a WekaPolicy stays `Running`. The operator log shows only `Error fetching results`. The real error is in the logs of the adhoc pods, named `weka-adhoc-<operation-or-policy-name>-<node-uid>` in the operation's namespace (cloud-helper JSON on stdout):

```bash
kubectl get pods -n <namespace> | grep weka-adhoc-<operation-or-policy-name>
kubectl logs -n <namespace> <weka-adhoc-pod>
```

Common causes: `dataNICsNumber` above the instance limit (see Sizing), and missing cloud permissions (see Credentials).
