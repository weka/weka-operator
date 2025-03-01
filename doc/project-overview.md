# Project overview

This project is a kubernetes operator for deploying and managing weka cluster

- Weka cluster(wekacluster crd) consist of set of weka containers(wekacontainer crd)
- Each wekacontainer represented by a pod
- Each pod contains a weka agent
- Weka agent is a custom container management system, and that agent runs weka linux containers(within a pod)

- Weka containers can be of multiple modes, representing cluster roles or auxilary functionality (refer to container_types.go for a defintion)
- Each pod's entrypoint is a weka_runtime.py script stored in configmap and mounted in pod to be used as entry point
- wekacontainer CR and correspondending pod names are always matching and can be used interchangeably

# weka container types (a copy form go code, api level has a string value)
```
const (
WekaContainerModeDist           = "dist"
WekaContainerModeDriversDist    = "drivers-dist"
WekaContainerModeDriversLoader  = "drivers-loader"
WekaContainerModeDriversBuilder = "drivers-builder"
WekaContainerModeCompute        = "compute"
WekaContainerModeDrive          = "drive"
WekaContainerModeClient         = "client"
WekaContainerModeDiscovery      = "discovery"
WekaContainerModeS3             = "s3"
WekaContainerModeNfs            = "nfs"
WekaContainerModeEnvoy          = "envoy"
WekaContainerModeAdhocOpWC      = "adhoc-op-with-container"
WekaContainerModeAdhocOp        = "adhoc-op"
```

# Wekacontainer lifecycle
- Wekacontainer is created by wekacluster controller, wekaclient controller or manually in some cases
  - wekacontainer is deployed in a same namespace as wekacluster
- on-deletion of a wekacontainer's pod weka_runtime will block and wait for wekacontainer controller to signal for pod to terminate
- signal can be done by multiple ways, but most explicit is to touch /tmp/.allow-stop or /tmp/.allow-force-stop
  - stop-flags do not cause pods to terminate, they only allow to continue when termination is detected. Whenever intention is to terminate pod - it should be done explicitly along with writing stop-signals
  - when pod is deleted it will immediately be re-created by controller with updated config(if changed), name remains same as before,  hence delete flow should use blocking kubectl delete flow and not explicitly poll for absence of container
- wekacontainers bound to a cluster can be found by pod/wekacontainer labels weka.io/cluster-id=CLUSTER_ID
  - wekacontainer also has label weka.io/mode=mode
    - mode sometimes referenced as a role
  - CLUSTER_ID in this case represented by k8s metadata.uid of wekacluster CR
- pod belonging to wekacontainer should never be force deleted, only proper graceful termination that will wait for as long as needed
- pod belonging to wekacontainer named exactly as a wekacontainer, and when re-created re-uses same name. Pod re-created shortly after termination
### in-pod file-flags
- /tmp/.allow-force-stop - allows to force stop weka_runtime.py for a quick exit
- /tmp/.cancel-debug-sleep - allows to cancel debug sleep on exit
  This flags can be set via exec in pod
# Overrides
- wekacontainer, wekacluster and wekaclient CR has a field spec.overrides
- overrides should be updated only by patching with merge strategy, an example:
  - `kubectl patch wekacontainer container-nam -n default --type='merge' -p='{"status":{"debugSleepOnTerminate": 300}}'`