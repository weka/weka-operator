# WekaClient and Workload Guide

## Overview
This document explains how to deploy and manage WekaClient resources, install CSI drivers, and run workloads that utilize Weka storage in Kubernetes. It covers the entire workflow from client deployment to workload execution.

## WekaClient Resource
The WekaClient CR represents a group of client WekaContainers, similar to a DaemonSet. These clients enable filesystem mounting on nodes where they are deployed.

### Example WekaClient CR
```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaClient
metadata:
  name: CLUSTER_NAME-clients
  namespace: NAMESPACE
spec:
  image: quay.io/weka.io/weka-in-container:4.4.2.157-k8s.2
  imagePullSecret: "quay-io-robot-secret"
  driversDistService: "https://weka-drivers-dist.weka-operator-system.svc.cluster.local:60002"
  nodeSelector:
    weka.io/dedicated: "SAME_VALUE_AS_ON_CLUSTER_PROVISION"
  wekaSecretRef: weka-client-CLUSTER_NAME
  targetCluster:
    name: CLUSTER_NAME
    namespace: CLUSTER_NAMESPACE
  coresNum: 1
  network:
    deviceSubnets:
      - 10.200.0.0/16
```

### Key Configuration Fields
- `wekaSecretRef`: Reference to a secret created by the WekaCluster CR, follows format `weka-client-CLUSTER_NAME`
- `targetCluster`: The WekaCluster to connect to, specified by name and namespace
- `nodeSelector`: Should match the selector used for the cluster unless otherwise instructed
- `image`: Should match the image used for the WekaCluster unless otherwise instructed
- `network`: Optional for cloud environments, required for physical environments

### Port configuration
No need to specify unless have to use custom ports, default is equivalent to:
```
portRange:
  basePort: 45000
```
Which will search for available port starting from 45000

## CSI Driver Installation
To enable persistent volume claims with Weka storage, install the CSI driver:

```bash
helm upgrade csi-CLUSTER_NAME -n NAMESPACE --create-namespace -i https://github.com/weka/csi-wekafs/releases/download/v2.7.1/csi-wekafsplugin-2.7.1.tgz -set logLevel=6 --values csi_values.yaml
```

### CSI Values Example
```yaml
pluginConfig:
  allowInsecureHttps: true
  skipGarbageCollection: true
controllerPluginTolerations:
 - operator: Exists
nodePluginTolerations:
 - operator: Exists
controller:
  nodeSelector:
    "client-node-selector": "client-node-selector"
node:   
  nodeSelector:
    "client-node-selector": "client-node-selecor"
csiDriverName: CLUSTER_NAME.weka.io
```

## Storage Provisioning

### Storage Class Creation
```yaml
allowVolumeExpansion: true
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: weka-CLUSTER_NAME-forcedirect
parameters:
  capacityEnforcement: HARD
  csi.storage.k8s.io/controller-expand-secret-name: weka-csi-CLUSTER_NAME
  csi.storage.k8s.io/controller-expand-secret-namespace: CLUSTER_NAMESPACE
  csi.storage.k8s.io/controller-publish-secret-name: weka-csi-CLUSTER_NAME
  csi.storage.k8s.io/controller-publish-secret-namespace: CLUSTER_NAMESPACE
  csi.storage.k8s.io/node-publish-secret-name: weka-csi-CLUSTER_NAME
  csi.storage.k8s.io/node-publish-secret-namespace: CLUSTER_NAMESPACE
  csi.storage.k8s.io/node-stage-secret-name: weka-csi-CLUSTER_NAME
  csi.storage.k8s.io/node-stage-secret-namespace: CLUSTER_NAMESPACE
  csi.storage.k8s.io/provisioner-secret-name: weka-csi-CLUSTER_NAME
  csi.storage.k8s.io/provisioner-secret-namespace: CLUSTER_NAMESPACE
  filesystemName: default
  mountOptions: forcedirect
  volumeType: dir/v1
provisioner: .weka.io
reclaimPolicy: Delete
volumeBindingMode: Immediate
```

### Persistent Volume Claim
```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: CLUSTER_NAME-goader-pvc
  namespace: CLUSTER_NAMESPACE
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: weka-CLUSTER_NAME-forcedirect
  resources:
    requests:
      storage: 500Gi
```

## Running Workloads
Sample workload utilizing Weka storage through PVC:

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: goader-smallios-CLUSTER_NAME
  namespace: CLUSTER_NAMESPACE
  labels:
    app: goader-smallios-CLUSTER_NAME
    cluster: CLUSTER_NAME
spec:
  updateStrategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 100%
  selector:
    matchLabels:
      app: goader-smallios-CLUSTER_NAME
      cluster: CLUSTER_NAME
  template:
    metadata:
      labels:
        app: goader-smallios-CLUSTER_NAME
        cluster: CLUSTER_NAME
    spec:
      nodeSelector:
        "client-node-selector": "client-node-selector"
      containers:
        - name: goader
          imagePullPolicy: Always
          image: public.ecr.aws/weka/goader:latest
          env:
            - name: GOADER_PARAMS
              value: "-wt=2 -rt=2 --body-size=128KiB --show-progress=False --max-requests=50000 --mkdirs --url /data/small/${NODE_NAME}/NN/NN"
            - name: NODE_NAME
              valueFrom:
                fieldRef:
                  fieldPath: spec.nodeName
          volumeMounts:
            - name: goader-storage
              mountPath: /data
      volumes:
        - name: goader-storage
          persistentVolumeClaim:
            claimName: CLUSTER_NAME-goader-pvc
```

### Labels
when provisioned using targetCluster, containers that belong to wekaclient will be marked with label `weka.io/target-cluster-name: CLUSTER_NAME`

## Resource Cleanup
When removing workload resources, follow this order:
1. Delete workload and wait for completion
2. Delete PVC and wait for completion
3. Delete WekaClient CR (if not needed anymore)
4. Delete WekaCluster CR (if not needed anymore)