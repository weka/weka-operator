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
      # Builders will be scheduled on nodes matching these selectors AND discovered kernel/arch.
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

## Cordoned nodes

A cordon taints the node `node.kubernetes.io/unschedulable:NoSchedule`. By default that holds every
mode off the node, same as any other untolerated taint. Client-side modes — `client`, `drivers-loader`,
`drivers-dist`, `discovery`, and the `adhoc-op` modes — can instead be made to tolerate that taint, so
they keep running and keep being created on a cordoned node, by enabling `scheduling.tolerateCordon` in
a configuration `WekaPolicy`:

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
        tolerateCordon: true
```

Backends never tolerate the cordon, regardless of this setting.

This exists because a cordon means "no new user workloads here", not "no node infrastructure". A node
cordoned pending validation cannot pass a storage health check with no client on it, and if the client
cannot start because the node is cordoned, nothing ever uncordons it. Without the toleration, a
cordoned node that reboots loses its drivers-dist container and its drivers-loader pod, and every
client on it stalls in `WaitForDrivers` until someone uncordons by hand.

With `tolerateCordon` enabled, a cordon on its own does not keep a client off a node. `nodeSelector`
and any taint the client does not tolerate still do, and `skipClientsTolerationValidation`,
`skipClientNoScheduleToleration` and `skipAuxNoScheduleToleration` do not control this setting.

One gap remains: `drivers-builder` does not tolerate the cordon even when the setting is enabled. It is
not pinned to a node — it runs wherever its kernel/architecture `nodeSelector` matches — so it only
matters when *every* matching node is cordoned and the drivers for that kernel have not been built yet.
Uncordon one node to let the build through.

## See also

Distribution produces and serves the driver files. For how the operator then loads those
drivers into the kernel on each node (the per-node drivers-loader, the
`weka.io/drivers-loaded` annotation, and the frontend/backend priority model), see
[Drivers Loading](../operations/drivers-loading.md).