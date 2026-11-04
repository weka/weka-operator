Driver distribution is usually a part of operator install, i.e no need to install it unless also installing operator

Classic approach to build drivers is to deploy
- drivers-builder container, one per permutation of used weka versions and kernel/arch
- drivers-dist container, one
- service pointing to drivers-dist container
Example of such, handling multiple kernels, and having custom pre-run script

Important: No need to deploy multiple builders if no need to support multiple kernels or multiple weka versions
Important: Replace versions with your target wekaclient/wekacluster versions. Versions(images) on builders must match
```
apiVersion: weka.weka.io/v1alpha1
kind: WekaContainer
metadata:
  name: weka-drivers-dist
  namespace: weka-operator-system
  labels:
    app: weka-drivers-dist
spec:
  agentPort: 60001
  image: quay.io/weka.io/weka-in-container:4.4.2.144-k8s
  imagePullSecret: "quay-io-robot-secret"
  mode: "drivers-dist"
  name: dist
  numCores: 1
  port: 60002
---
apiVersion: v1
kind: Service
metadata:
  name: weka-drivers-dist
  namespace: weka-operator-system
spec:
  type: ClusterIP
  ports:
    - name: weka-drivers-dist
      port: 60002
      targetPort: 60002
  selector:
    app: weka-drivers-dist
---
apiVersion: weka.weka.io/v1alpha1
kind: WekaContainer
metadata:
  name: weka-drivers-builder-157
  namespace: weka-operator-system
spec:
  agentPort: 60001
  image: quay.io/weka.io/weka-in-container:4.4.2.157-k8s
  imagePullSecret: "quay-io-robot-secret"
  mode: "drivers-builder"
  name: dist # name of the weka container, will be removed in future towards slimmer distribution service
  numCores: 1
  uploadResultsTo: "weka-drivers-dist"
  port: 60002
  nodeSelector:
    weka.io/supports-backends: "true"
---
apiVersion: weka.weka.io/v1alpha1
kind: WekaContainer
metadata:
  name: weka-drivers-builder-157-ubuntu-1
  namespace: weka-operator-system
spec:
  agentPort: 60001
  image: quay.io/weka.io/weka-in-container:4.4.2.157-k8s
  imagePullSecret: "quay-io-robot-secret"
  mode: "drivers-builder"
  name: dist # name of the weka container, will be removed in future towards slimmer distribution service
  numCores: 1
  uploadResultsTo: "weka-drivers-dist"
  port: 60002
  nodeSelector:
    weka.io/supports-backends: "true"
    weka.io/kernel: "6.5.0-45-generic"
  overrides:
    preRunScript: "apt-get update && apt-get install -y gcc-12"

```
Ports, modes, cores configurations, container name(spec.name) must be preserved as in above snippets 


Current operator versions(starting from 1.6.0) support driver distribution deployment by policy, which will auto-create the above resources.
Example of driver distribution service policy:

```
apiVersion: weka.weka.io/v1alpha1
kind: WekaPolicy
metadata:
  name: weka-drivers
  namespace: weka-operator-system # Replace with your target namespace
spec:
  type: "enable-local-drivers-distribution"
  # Image and ImagePullSecret for the drivers-dist container and default for builders
  image: "quay.io/weka.io/weka-in-container:4.4.5.118-k8s.4" # Replace with your desired Weka image for the dist service
  imagePullSecret: "quay-io-robot-secret" # Replace with your image pull secret
  tolerations:
  - key: "example-key"
    operator: "Exists"
    effect: "NoSchedule"
  payload:
    interval: "1m" # How often to reconcile this policy
    driverDistPayload: # mandatory
      # Images to ensure IN ADDITION to these found by existing WekaCluster/WekaClients
      ensureImages:
        - "quay.io/weka.io/weka-in-container:4.4.2.157-k8s.2" # Example Weka image version for proactive building
        - "quay.io/weka.io/weka-in-container:4.4.5.118-k8s.4" # Another example
      # NodeSelectors to define which nodes this policy applies to for driver building.
      # One builder per image and kernel/arch/os across all selectors; it runs on any node that
      # matches one of the selectors and has that kernel/arch/os.
      nodeSelectors:
        - role: "worker-nodes"
          environment: "production"
        - custom-label: "drivers-build-pool"
      # Optional: Custom labels for kernel and architecture. Defaults to weka.io/kernel and weka.io/architecture
      # kernelLabelKey: "custom.io/kernel-version"
      # architectureLabelKey: "custom.io/arch"
      # Optional: Custom labels for the driver distribution service, kept empty if not specified, allowing to schedule on any node
      # distNodeSelector: {}
      # Optional: A script to run on builder containers after kernel validation but before the main build process
      builderPreRunScript: |
        #!/bin/sh
        apt-get update && apt-get install -y gcc-12
```

After deploy of policy `status.typesStatus.distService.serviceUrl` will contain the URL of the driver distribution service
This URL should be used in the WekaCluster and WekaClient CRs being deploy, as `spec.driversDistService`

drivers-builder wekacontainer's pods will be alive only during building, after building completed wekacontainer remains with no active pod
If wekapolicy is deleted - all created resources will be deleted as well

Build containers that belong to policy can be filtered by labels
`weka.io/policy-name=policy-name`
`weka.io/mode=drivers-builder`

Service can be found by:
`weka.io/policy-name: policy-name`

Dist container can be found by:
`weka.io/policy-name: policy-name`
`weka.io/mode: drivers-dist`

Policies `status.status` will be set to Done once completed

## NixOS nodes

NixOS is not FHS-compliant: hosts have no `/lib/modules` or `/usr/src`, everything lives under
`/nix/store`. This needs a NixOS host prerequisite, a dedicated builder image, and host mounts
in place of the generic host-mount path used for other distros.

Host prerequisite: the running kernel's headers package in the system profile, which places
them at `/run/current-system/sw/lib/modules/$(uname -r)/build`, the same shape as a distro's
`/lib/modules/$(uname -r)/build`:
```
environment.systemPackages = [ config.boot.kernelPackages.kernel.dev ];
```

The builder image is chosen by the gcc major that built the node's running kernel: discovery
reads `/proc/version` and reports the node's os as `nixos-gcc<major>` (e.g. `nixos-gcc15`), and
the operator looks up a builder image pinned to that same gcc major. Configure one entry per
gcc major you need to support:
```yaml
builderImages:
  nixos:
    gcc15: "quay.io/weka.io/weka-drivers-build-images:builder-nixos-gcc15-v2"
```

A node whose gcc major has no configured entry fails builder/loader pod creation with an
explicit error rather than falling back to another image, since a driver built with the wrong
gcc major won't load against the running kernel.

drivers-builder/drivers-loader pods on NixOS nodes mount, read-only:
- host `/nix/store` -> `/host/nix/store`
- host `/run/current-system` -> `/host/current-system` (headers at `sw/lib/modules/$(uname -r)/build`, in-tree modules at `kernel-modules/lib/modules/$(uname -r)`)

Only the builder image's `wekactl` understands this layout, and the loader also runs in the
builder image, so a configuration WekaPolicy enabling `forceBuilderCli` is required:
```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaPolicy
metadata:
  name: weka-drivers-config
  namespace: weka-operator-system
spec:
  payload:
    configurationPayload:
      drivers:
        forceBuilderCli: true
```

Both the builder (`weka driver pack`) and the loader (`weka driver download` / `weka driver install`) pass `--kernel-build-id nixos-gcc<major>` (e.g. `--kernel-build-id nixos-gcc15`), so NixOS drivers never collide with another distro's build, or another gcc major's build, at the same upstream kernel version.

## Placement

For every WekaContainer created without a node pinned (drivers-builder, drivers-dist, and aux or adhoc helpers created with only a
`nodeSelector`), the operator splits the nodes that match its `nodeSelector` and affinity into eligible (Ready,
no taint the pod does not tolerate, and not cordoned unless
[`scheduling.clientsTolerateAll`](#cordoned-and-tainted-nodes) or the deprecated `tolerateCordon` is set) and ineligible. It picks one eligible
node for node discovery, preferring nodes that already have a usable discovery result (from the node's current boot, at the current schema), so a retry does not start discovery on another node. drivers-builder, drivers-dist and their ad-hoc helpers take a random one, so builders spread and a retry after Pending can try another node; any other container takes the first. Then:

| Container | `clientsTolerateAll` | Pod placement |
|---|---|---|
| drivers-builder, drivers-dist and their ad-hoc helpers | off | pinned to the picked node with a required `metadata.name In [node]` field requirement, because a drivers policy that tolerates every taint (`[{operator: Exists}]`), or a helper's copy of its parent's catch-all tolerations, could otherwise bind it to a NotReady node |
| any container that gets the flag's `operator: Exists` (drivers-builder, drivers-dist, client, aux and adhoc helpers) | on | floats; a required `weka.io/not-ready DoesNotExist` requirement keeps it off nodes the operator labelled NotReady (see [NotReady nodes](#notready-nodes)) |
| any other container (backends, and the helpers without the flag) | any | not constrained: the scheduler places it, as before |

The container is pinned to its node (`status.nodeAffinity`) once the pod is bound. A pod that stays
Pending past `deleteUnschedulablePodsAfter` is deleted, and the next pass picks again. While no matching node is
eligible there is no pod and no discovery container; for policy containers the policy also gets a
`NoEligibleNode` warning event (at most every 5 minutes per container).

On each run, a policy moves its pinned containers when the node is gone or NotReady, or when it is
cordoned (without `clientsTolerateAll` or `tolerateCordon`) or carries an untolerated taint and no live pod is bound there:

| Container | On a move |
|---|---|
| drivers-dist | deleted, recreated under a new generated name |
| drivers-builder, not Completed | deleted, `DriversBuilderReplaced` warning event on the policy; the next run recreates it under a new generated name (the old object stays until its node returns and its pod finishes terminating) |
| drivers-builder, Completed | never moved by the policy: its pod is already gone (if its Node object is deleted, the container is removed and rebuilt) |

### NotReady nodes

With `clientsTolerateAll` the operator runs a Node controller that sets the label `weka.io/not-ready=true` on
every node whose Ready condition is not `True` (NotReady or unreachable) and removes it when the node is
Ready again. Only the Ready condition counts: a cordon or custom taint never sets it. Without the flag the
controller only removes labels left behind. The controller also watches the configuration `WekaPolicy`: changing the flag re-evaluates every node at once, and a 5-minute resync is only the fallback.

On a NotReady node a drivers-builder or drivers-dist pod is not deleted by the container reconcile; the policy
moves the container as described above, and the drivers-loader waits for the node to return. The moved
container's deletion finishes only once the node is Ready again when the node is unreachable (Ready `Unknown`);
its replacement is created at once under a new name and does not wait for it.

## Tolerations

The policy's `tolerations` apply to its drivers-builder and drivers-dist containers.
`scheduling.clientsTolerateAll` adds `operator: Exists` to them, and to the node discovery container
(`weka-dsc-<node>`), which otherwise copies the tolerations of whichever container created it. Cordon is
governed only by `scheduling.clientsTolerateAll` or the deprecated `scheduling.tolerateCordon` (see
[Cordoned and tainted nodes](#cordoned-and-tainted-nodes)).

## Cordoned and tainted nodes

By default a cordon (`node.kubernetes.io/unschedulable:NoSchedule`) and any taint a pod does not
tolerate hold it off the node. Enable `scheduling.clientsTolerateAll` in a configuration `WekaPolicy` to
have clients run on their nodes regardless, like a DaemonSet that tolerates every taint:

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaPolicy
metadata:
  name: configuration
  namespace: weka-operator-system
spec:
  payload:
    configurationPayload:
      scheduling:
        clientsTolerateAll: true
```

It applies by container mode, whoever created the container: `client`, `drivers-loader`, `drivers-dist`,
`drivers-builder`, `discovery`, and the `adhoc-op` modes except drive signing, drive discovery and
ssdproxy kernelize. Each of them gets an `operator: Exists` toleration on top of its own tolerations and
keeps running and being created on cordoned nodes. WekaClients create client containers on every node
their `nodeSelector` matches, whatever its taints. Backends, and the excluded adhoc ops, are never
affected. A drain of such a node may not complete, since the operator keeps recreating these pods there.

This exists because a cordon or a validation taint means "no new user workloads here", not "no node
infrastructure". A node held back pending validation cannot pass a storage health check with no client
on it, and the client cannot start without its drivers and discovery. `drivers-builder` is covered for a
different reason: it is placed on any node its kernel/architecture `nodeSelector` matches, so a node pool
brought up entirely cordoned or tainted on a kernel nobody has built for yet has no host to build on.

`scheduling.tolerateCordon` is deprecated and keeps its behaviour: the same modes tolerate the cordon
only (not other taints), and WekaClients create client containers on cordoned nodes. `clientsTolerateAll`
includes it. Neither is controlled by `skipClientsTolerationValidation`, `skipClientNoScheduleToleration`
or `skipAuxNoScheduleToleration`.

## See also

Distribution produces and serves the driver files. For how the operator then loads those
drivers into the kernel on each node (the per-node drivers-loader, the
`weka.io/drivers-loaded` annotation, and the frontend/backend priority model), see
[Drivers Loading](../operations/drivers-loading.md).