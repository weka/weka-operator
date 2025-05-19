This tests functionality of global PVC, and PVC migration from PVC setup to local setup
Use weka-operator-system for everything

- delete existing wekacluster/wekaclient CRs, if any exist in cluster(all namespaces)
- delete existing wekapolicies, if any exist in cluster
- delete any wekacontainers if remain in cluster, of all types, across all namespaces
- install CSI for NFS, using pre-created secret `weka-csi-test-nfs-pywe1lgz` which uses external weka cluster, in weka-operator-system of the cluster above. use `nfs-csi-internal.weka.io` as driver name, and `weka-nfs-sc-internal` as storage class name
- find nodes that run controllers label (pod label for controller is `app:weka-nfs-controller`) where weka-nfs is a release name, and install nfs-common and rpcbind on them
  - use ssh with root@nodeName user to connect to nodes by their name (i.e kubectl get node) names, and we are using all/any nodes in this test
- provision nfs storage class
  - make sure to use in storage class, as on any other weka nfs storage class:
```
filesystemName: default
volumeType: dir/v1
```
- create 3TiB pvc using this storage class, in weka-operator-system namespace, wait for PVC to be bound, name it `weka-nfs-pvc`
  - this is weka PVC despite using NFS, make sure to follow instructions for weka PVC, specifically - storage class must contain reference to secrets
- after cleanup and pvc create, re-deploy operator using version OLD_OPERATOR_VERSION setting global param localDataPvc=weka-nfs-pvc
- deploy legacy set of drivers distribution
  - When deploying driver builder container, always use:
  ```
        preRunScript: |
          #!/bin/sh
          apt-get update && apt-get install -y gcc-12
  ```
- ensure policy for drive signing of all not mounted, create policy if does not exist
- provision a weka cluster, 7 compute containers, 7 drive containers, 1 drive per drives container, 1 hotspare
- wait for cluster to become ready
- provision wekaclient
- this is actual starting point of a test, before was preparation
- reinstall operator using version NEW_OPERATOR_VERSION, still setting localDataPvc=weka-nfs-pvc
- patch pvc of the dist wekacontainer to have reference to PVC created above, as it wont be auto-populated
- validate that wekacontainers belonging to cluster and client got PVC populated on them
  - wekacluster and wekaclient will not be updated, only their containers
- patch dist wekacontainer to have `migrateOutFromPvc` override set to true
- reinstall operator using version NEW_OPERATOR_VERSION this time, setting localDataPvc to empty string in values
- delete pod belonging to dist wekacontainer
- wait for pod to be recreated and ensure that PVC field is set to nil after re-create
- delete all wekacontainers that belong to  cluster and client, deletion will take time as it will be rolling by default, despite marking all
    - use --wait=false flag and continue without confirming deletion
    - Pay attention, it can take long time. If nothing progressed for 10 minutes - then abort, but there should be slowly rotation of all pods, despite deleting all at once it will be rolling, controlled by operator
- wait for all containers that belong to cluster to be re-created and their pvc set to nil
- clusters and client should use 10.200.0.0/16 subnet
- cluster, clients, drivers components should use WEKA_IMAGE image

use `https://github.com/weka/csi-wekafs/releases/download/v2.7.2/csi-wekafsplugin-2.7.2.tgz` for CSI install
derive parameters from my request if execution asks for more, ask for what cannot be derrived

Params(and their defaults):
  - New operator version: v1.6.0-dev.8 (NEW_OPERATOR_VERSION)
  - Old operator version: v1.5.0 (OLD_OPERATOR_VERSION)
  - Weka image to use: quay.io/weka.io/weka-in-container:4.4.5.118-k8s.4 (WEKA_IMAGE)