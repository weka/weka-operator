Basic yaml of cluster provision:
This is a minimalistic example, for any testing/development purpose start off this yaml
IMPORTANT: Do not remove any fields and only adjust if needed
```
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: demo-provision
  namespace: NAMESPACE
spec:
  template: dynamic
  dynamicTemplate:
    computeContainers: 10
    driveContainers: 10
    computeCores: 2
    driveCores: 2
    computeHugepages: 10000
    numDrives: 2
  hotSpare: 1
  leadershipRaftSize: 9
  bucketRaftSize: 9
  redundancyLevel: 4
  gracefulDestroyDuration: 0s
  startIoConditions:
    minNumDrives: 20
  overrides: {}
  image: quay.io/weka.io/weka-in-container:4.4.2.157-k8s.2
  nodeSelector:
    weka.io/dedicated: "net-migration"
  driversDistService: "https://weka-drivers-dist.weka-operator-system.svc.cluster.local:60002"
  imagePullSecret: "quay-io-robot-secret"
```

Note, gracefulDestroyDuration is overriden and set to 0, default is 24h
Setting to 0s allows for quicker cluster deletion
When wekacluster is deleted with graceful termination > 0, all containers will move to status "paused" (.status.status on wekacontainers)
At this point there should be no associated pods
When timeout expires (from deletion timestamp on wekacluster) - cluster will fully destroy wekacontainers
Changing gracefulDestroyDuration to 0s will cause immediate deletion of wekacontainers by moving them to destroying state


### Additional flags
spec.dynamicTemplate.s3Containers = provisions cluster with s3 support
minNumDrives should be in the range of 80% of driveContainers * numDrives

# Expand
Expand is done by increasing number of containers of appropriate type

### Networking
When cluster is deployed on physical environment, network section should be specified

On physical environment ethDevice, or ethDevices, or deviceSubnets should be used
In rare cases, when specific network device should be used in UDP mode - udpMode should be set to true while still specifying a single device via ethDevice

Optional network section(under spec):
```yaml
spec:
    network:
        deviceSubnets:
            - 10.200.0.0/16
```

Snippet from kubectl explain wekacluster.spec.network
```yaml
KIND:     WekaCluster
VERSION:  weka.weka.io/v1alpha1

RESOURCE: network <Object>

DESCRIPTION:
     weka cluster network configuration

FIELDS:
   aws	<Object>
      deviceSlots	<[]integer>
   deviceSubnets	<[]string>
   ethDevice	<string>
   ethDevices	<[]string>
   ethSlots	<[]string>
   gateway	<string>
   udpMode	<boolean>
```

# More optional fields
When cluster has more then 20 drive containers it is best to set
```yaml
spec:
  redundancyLevel: 4
  stripeWidth: 16
```
This will ensure largest stripe width on erasure coding/raid level and enable +4 protection from failures
A default is 2 for redundancyLevel and auto-calculated for stripeWidth


After cluster was provisioned, a process can be observed by polling kubernetes api against wekacluster object, status.status field should reach Ready
If it does not reach "Ready" within 10 minutes - consider it failed