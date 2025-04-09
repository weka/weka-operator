# Weka Operator Secrets Management

## Overview

The Weka operator creates and manages several Kubernetes secrets for each Weka cluster. These secrets store credentials and connection information required for various components to interact with the Weka cluster. This document explains each secret's purpose, content, and how to manage them.

## Secrets Created by the Operator

For each WekaCluster, the operator creates four distinct secrets:

1. **Operator Secret**: Used by the operator to authenticate with the Weka cluster
2. **User Admin Secret**: Created for administrators to access the Weka cluster
3. **Client Secret**: Used by Weka clients to connect to the Weka cluster
4. **CSI Secret**: Used by the CSI plugin to provision and manage storage

### Operator Secret

This secret contains credentials used by the operator itself to perform administrative operations on the Weka cluster.

**Secret name format**: `weka-operator-<cluster-uid>`

**Contents**:
- `username`: Operator-specific admin user
- `password`: Password for the operator user
- `org`: Organization name (typically "Root")
- `join-secret`: Token for nodes to join the cluster

**Example**:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: weka-operator-b0dbe115-3d64-465a-ba5c-d3f50c2de60e
  namespace: <namespace>
  ownerReferences:
  - apiVersion: weka.weka.io/v1alpha1
    kind: WekaCluster
    name: <cluster-name>
    uid: <cluster-uid>
type: Opaque
data:
  join-secret: <base64-encoded-token>
  org: <base64-encoded-org>
  password: <base64-encoded-password>
  username: <base64-encoded-username>
```

**Create manually with kubectl**:

```bash
kubectl create secret generic weka-operator-<cluster-uid> \
  --from-literal=username=weka-operator-<cluster-uid-short> \
  --from-literal=password=<generated-password> \
  --from-literal=org=Root \
  --from-literal=join-secret=<join-token>
```

### User Admin Secret

This secret contains credentials for administrative access to the Weka cluster, intended for user/administrator use.

**Secret name format**: `weka-cluster-<cluster-name>`

**Contents**:
- `username`: Admin username
- `password`: Admin password
- `org`: Organization name (typically "Root")

**Example**:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: weka-cluster-<cluster-name>
  namespace: <namespace>
  ownerReferences:
  - apiVersion: weka.weka.io/v1alpha1
    kind: WekaCluster
    name: <cluster-name>
    uid: <cluster-uid>
type: Opaque
data:
  org: <base64-encoded-org>
  password: <base64-encoded-password>
  username: <base64-encoded-username>
```

**Create manually with kubectl**:

```bash
kubectl create secret generic weka-cluster-<cluster-name> \
  --from-literal=username=weka<cluster-uid-short> \
  --from-literal=password=<generated-password> \
  --from-literal=org=Root
```

### Client Secret

This secret contains credentials and connection information used by Weka clients to connect to the Weka cluster.

**Secret name format**: `weka-client-<cluster-name>`

**Contents**:
- `username`: Client username
- `password`: Client password
- `org`: Organization name (typically "Root")
- `join-secret`: Token for clients to join the cluster

**Example**:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: weka-client-<cluster-name>
  namespace: <namespace>
  ownerReferences:
  - apiVersion: weka.weka.io/v1alpha1
    kind: WekaCluster
    name: <cluster-name>
    uid: <cluster-uid>
type: Opaque
data:
  join-secret: <base64-encoded-token>
  org: <base64-encoded-org>
  password: <base64-encoded-password>
  username: <base64-encoded-username>
```

**Create manually with kubectl**:

```bash
kubectl create secret generic weka-client-<cluster-name> \
  --from-literal=username=wekaclient<cluster-uid-short> \
  --from-literal=password=<generated-password> \
  --from-literal=org=Root \
  --from-literal=join-secret=<join-token>
```

### CSI Secret

This secret contains credentials and connection information used by the CSI plugin to provision and manage storage.

**Secret name format**: `weka-csi-<cluster-name>`

**Contents**:
- `username`: CSI service account username
- `password`: CSI service account password
- `organization`: Organization name (typically "Root")
- `endpoints`: Comma-separated list of Weka API endpoints in format `<ip>:<port>`
- `scheme`: API access scheme (typically "https")
- `nfsTargetIps`: IP addresses for NFS targets

**Example**:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: weka-csi-<cluster-name>
  namespace: <namespace>
  ownerReferences:
  - apiVersion: weka.weka.io/v1alpha1
    kind: WekaCluster
    name: <cluster-name>
    uid: <cluster-uid>
type: Opaque
data:
  endpoints: <base64-encoded-endpoints>
  nfsTargetIps: <base64-encoded-nfs-ips>
  organization: <base64-encoded-org>
  password: <base64-encoded-password>
  scheme: <base64-encoded-scheme>
  username: <base64-encoded-username>
```

**Create manually with kubectl**:

```bash
kubectl create secret generic weka-csi-<cluster-name> \
  --from-literal=username=wekacsi<cluster-uid-short> \
  --from-literal=password=<generated-password> \
  --from-literal=organization=Root \
  --from-literal=endpoints=<ip1>:35000,<ip2>:35100,... \
  --from-literal=scheme=https \
  --from-literal=nfsTargetIps=<ip>
```

## Usage in WekaClient Resources

When using the WekaClient Custom Resource to connect to a Weka cluster:

1. **Operator-provisioned clusters**: The WekaClient will automatically use the client secret created by the operator when `targetCluster` is specified.

2. **Manual connection to non-operator clusters**: When direct IPs are specified in the WekaClient configuration instead of `targetCluster`, you must create the CSI secret manually. The secret may need to include a `join-secret` (also called `join-token` on the Weka side) depending on whether this functionality is enabled on the cluster:
   - Systems provisioned by the operator have join-secret enabled by default
   - Systems provisioned outside the operator typically do not have join-secret enabled by default

## Secret Lifecycle Management

The operator manages the entire lifecycle of these secrets:
- Creates them during cluster provisioning
- Updates them when necessary (e.g., after password changes)
- Deletes them when the WekaCluster resource is deleted

All secrets have owner references to the WekaCluster resource, ensuring proper garbage collection.

## Manual Secret Management

While the operator typically manages these secrets automatically, in some scenarios (like connecting to external clusters or troubleshooting), you may need to create or modify them manually.

**Important**: When manually creating secrets, ensure you maintain the expected formats and naming conventions described above.