WekaClient CR represents group of clients wekacontainers, similar to daemonset
In order to be able to mount filesystem such container should present on the node

wekaclient might be scheduled on same set of nodes as backends, or dedicated nodes just for clients
when referencing as "client" a meaning usually is a wekacontainer of type weka.io/mode=client which provisioned by wekaclient CR
same as with wekacluster wekacontainers, wekaclient's wekacontainers provision a pod with same name as wekaclient CR
example of wekaclient CR:

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
  portRange:
    basePort: 45000
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
When provisioning keep all fields as is, only adjust values as needed, but not remove existing fields, unless explicitly instructed
network section is optional, same as with wekacluster CR - mandatory for physical environment, optional for other environments
Created by wekaclient containers are named as wekaclient_CR_NAME-NODE_NAME, so name of a client wekacontainer/pod can be used to determine nodename

wekaSecretRef - a reference to a secret created by wekacluster CR, in format of weka-client-CLUSTER_NAME
targetCluster - a wekacluster to connect to, by name and namespace
nodeSelector - unless instructed explicitly and otherwise, same node selector as one used for cluster should be used
image - unless instructed explicitly and otherwise, same image as the one used for wekacluster should be used


wekaclient will expand into set of wekacontainer with weka.io/mode=client
once containers provisioned, they will be able to mount filesystems from wekacluster, assuming there is a matching CSI
wekaclient's wekacontainer's and pods can be found by labels:
`weka.io/client-name: wekaclient_cr_name` + `weka.io/mode: client`
Note: wekaclient provisioned weka containers do not have weka.io/cluster-name label, as it's not part of cluster, but instead references the cluster
Filtering of wekaclient containers could be done by weka.io/client-name=WEKACLIENT_RESOURCE_NAME and weka.io/model=client labels
In case wekaclient relied on targetCluster (using JoinIps is a rare alternative), wekaclient resource will also have weka.io/target-cluster-name label, which can be used to find cluster's client containers

Wekaclient itself does not have explicit status field that indicates health, instead wekacontainers should be polled, or wide status field can be used to get summaries:
```
kubectl  get wekaclient -o wide -n infra weka-infra-clients
NAME                 STATUS   TARGET CLUSTER   CORES   JOIN IPS   CONTAINERS(A/C/D)
weka-infra-clients            weka-infra       1                  4/4/4
```
Containers is a part of status in printer, which consist of aggregated counter metrics, that also can be used to find out if wekaclient scheduled right(desired) and whether it connected to cluster(active)
```
"status": {
        "lastAppliedSpec": "e167d5b273161f6a8c395fd79ff270650b2b90a15430c3937d338b037c4c368b",
        "printer": {
            "containers": "4/4/4"
        },
        "stats": {
            "containers": {
                "active": 4,
                "created": 4,
                "desired": 4
            }
        }
    }
```

To install CSI driver
```bash
helm upgrade csi-CLUSTER_NAME -n NAMESPACE --create-namespace -i  https://github.com/weka/csi-wekafs/releases/download/v2.7.1/csi-wekafsplugin-2.7.1.tgz -set logLevel=6 --values csi_values.yaml
```

csi_values.yaml should be formed as such:
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
nodeSelector on both node and controller MUST match values that were used on the level of wekaClient CR AIMUST
csiDriverName must match format of CLUSTER_NAME.weka.io
For example, if targetCluster is cluster-dev, then csiDriverName should be cluster-dev.weka.io

Once wekaclient CR and CSI are installed it is possible to move on to provisioning a workload
Running workload requires
- Creating a storage class
- Creating a PVC
- Creating an actual workload

Unless instructed explicitly for specific values to override, use storage class as following, replacing CLUSTER_NAME and CLUSTER_NAMESPACE with actual values
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

After storage class is created, create and apply a PVC, for example:
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

Ensure that PVC is bound(wait up to 2 minutes, if not bound - there is an issue)

After PVC is bound, it is possible to schedule workload, for example:
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
node selector on workload should match node selector used on wekaclient CR
if goader pods reached Running state, it is working as expected
If they are stuck in Pending or other state - something is wrong

When CSI mounts PVC on node, such mount should be visible on node as:
default on /var/lib/kubelet/pods/f6e76cea-523d-49e7-aaf7-99750e4571f0/volumes/kubernetes.io~csi/pvc-434edd80-6581-44a9-82ce-0a8d363075c3/mount type wekafs


When deleting workload-related resources, they must be deleted in following order:
- Delete workload and wait for completion
- Delete pvc and wait for completion
- Delete wekaclient CR and wait for completion (if not needed anymore)
- Delete wekacluster CR and wait for completion (if not needed anymore)