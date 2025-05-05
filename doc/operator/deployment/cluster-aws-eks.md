# WEKA Cluster Provisioning In AWS (EKS) using the Weka Operator

## EKS cluster Provisioning
Using bliss(tool for provisioning K8s cluster), required flags:
- additional subnet
- iam role for the node group that should contain:
  - "AmazonEC2ContainerRegistryPullOnly"
  - "AmazonEKSWorkerNodeMinimalPolicy"
  - "AmazonEKS_CNI_Policy"
- iam role for the eks cluster that should contain:
  - "AmazonEKSBlockStoragePolicy"
  - "AmazonEKSClusterPolicy"
  - "AmazonEKSComputePolicy"
  - "AmazonEKSLoadBalancingPolicy"
  - "AmazonEKSNetworkingPolicy"

When using bliss it is expected to have such role pre-created
 
For EKS provisioning driversDistService should be set to https://drivers.weka.io
No need to provision in-cluster driver builder/distribution services

```bash
bliss provision aws-eks \
--subnet-id PROVISIONING_SUBNET \
--subnet-id ALB_ADDITIONAL_SUBNET \
--security-groups SECURITY_GROUPS \
--region REGION \
--key-pair-name KEY_PAIR_NAME \
--eks-node-group-iam-role-arn NODE_GROUP_ARN_ROLE \
--eks-cluster-iam-role-arn EKS_CLUSTER_ARN_ROLE \
--bliss-template-file /path/to/template.yaml --cluster-name EKS_CLUSTER_NAME \
--tag Owner=OWNER --tag TTL=TTL \
--kubeconfig /path/to/kubeconfig-EKS_CLUSTER_NAME 
```
Notes:
- Owner is in format user@domain.example and TTL is in format of duration "3h"
- Common approach is to use same CLUSTER_NAME as for wekacluster for EKS_CLUSTER_NAME, but they dont have to be same
- Note, same key(subnet-id) used for two subnets with different meaning. Order is important
- Bliss will save kubeconfig for cluster into specified path and it should be used on following invocations of kubectl, or any other method of communicating with k8s

Bliss template file describes node groups that will be provisioned and attached to EKS, as following examples:

- Bliss template for converged deployment, i.e clients and backends on the same nodes:
```yaml
nodes:
    - name: Converged
      instanceType: i3en.3xlarge
      initialSize: 6
      labels:
        key: value
      hugepages: 8000
cloud: aws
```

- Bliss template for separate clients  and backends on different nodes:
```yaml
nodes:
    - name: Backends
      instanceType: i3en.3xlarge
      initialSize: 6
      labels:
        key: value
      hugepages: 8000
    - name: Clients
      instanceType: c5.9xlarge
      initialSize: 2
      labels:
        key: value
      hugepages: 8000
cloud: aws
```

If using mix of multiple instance types, use appropriate dataNICsNumber for each instance type/scheduling by appropriate node selectors(and set this labels during provision)

On bliss-provisioned cluster, prior to install of operator - secrets to download from quay.io should be added
This can be done by another bliss command:
It is assumed and expected that environment that executes it has `QUAY_USERNAME` and `QUAY_PASSWORD` env vars, following command will create `quay-io-robot-secret` secret in `default` and `weka-operator-system` namespaces
```bash
bliss k8s ensure-secret quay --cluster-name EKS_CLUSTER_NAME --username $QUAY_USERNAME --password $QUAY_PASSWORD --namespaces default --namespaces weka-operator-system
```

### WekaCluster ports

For EKS provision weka cluster base port needs to be overriden and set as
```
  ports:
    basePort: 15000
```

### Cleanup
To delete cluster provisioned using bliss, use `bliss delete CLUSTER_NAME` command

# WekaCluster/Client Pre-requisites

### Install operator
Before installing weka resources(policies including) operator should be installed, no special instructions for EKS

### Ensure Nics
In EKS setup each weka core (both clients and backends) needs a dedicated NIC(AWS ENI), total amount of cores cannot be higher then number of available NICs.
Note, that one NIC should be reserved for NIC, so considering i3en.3xlarge has 4 ENIs, you can have 3 data NICs.
For above example of converged configuration, you will need to set dataNICsNumber to 3
```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaPolicy
metadata:
  name: ensure-nics-manual-op
  namespace: default
spec:
  type: "ensure-nics"
  image: quay.io/weka.io/weka-in-container:4.4.5.118-k8s.4
  imagePullSecret: "quay-io-robot-secret"
  payload:
    ensureNICsPayload:
      type: aws
      dataNICsNumber: 3
```

### Sign Drives (backends only)
```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaPolicy
metadata:
  name: sign-drives
  namespace: weka-operator-system # Replace with your namespace
spec:
  type: sign-drives
  imagePullSecret: "quay-io-robot-secret" # Default image pull secret
  payload:
    signDrivesPayload:
      type: "aws-all"
```

# Commonly used instance types (if not listed, use public AWS documentation)
- i3en.3xlarge - 3 data NICs, 2 drives, 3 available for weka cores
- i3en.6xlarge - 7 data NICs, 2 drives, 7 available for weka cores
- c5.9xlarge - 7 data NICs, no drives
