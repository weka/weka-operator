# WEKA Cluster Provisioning In AWS (EKS) using the Weka Operator

## Overview
This document details the differences of provisioning a Weka cluster in EKS.

## EKS cluster Provisioning
Using bliss, the additional flags are:
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
 
<br>**Note**: need to use the appropriate bliss template
<br>The main difference in this template is that for driversDistService we use https://drivers.weka.io

<br>Assuming you have a profile named `devkube` configured, the bliss command should look like this:
```bash
AWS_PROFILE=devkube ./bliss provision aws-eks \
--subnet-id <subnet-id1> \
--subnet-id <subnet-id2> \
--security-groups <sg>\
--region <region> \
--key-pair-name <key-pair-name> \
--eks-node-group-iam-role-arn <arn1> \
--eks-cluster-iam-role-arn <arn2> \
--template aws_eks_small --cluster-name <cluster-name>
````

## Ensure NICs
Prior to provisioning the WEKA cluster, ensure that the network interfaces (NICs) are set. 
Depends on the network configuration of the cluster you will need to set the NICs.
For example, lets say you with to have 2 cores for compute and 1 for drive you can use the following example:
```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaManualOperation
metadata:
  name: ensure-nics-manual-op
  namespace: default
spec:
  action: "ensure-nics"
  image: quay.io/weka.io/weka-in-container:4.4.5.118-k8s.4
  imagePullSecret: "quay-io-robot-secret"
  payload:
    ensureNICsPayload:
      type: aws
      nodeSelector:
        weka.io/supports-backends: "true"
      dataNICsNumber: 3
```
If you also want to have a client with several cores on the same nodes, you will need to increase the dataNICsNumber accordingly.
<br>If you have a separate nodes for clients, you can have additional yaml where you can set the dataNICsNumber to fit the cores number you want and update the node selector.