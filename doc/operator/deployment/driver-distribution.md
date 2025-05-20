Driver distribution is usually a part of operator install, i.e no need to install it unless also installing operator

Classic approach to build drivers is to deploy
- drivers-builder container, one per permutation of used weka versions and kernel/arch
- drivers-dist container, one
- service pointing to drivers-dist container
Example of such, handling multiple kernels, and having custom pre-run script

Important: No need to deploy multiple builders if no need to support multiple kernels or multiple weka versions
Importnat: Replace versions with your target wekaclient/wekacluster versions. Versions(images) on builders must match
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
      # Images to ensure IN ADDDITION to these found by existing WekaCluster/WekaClients
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

After deploy of policy `status.typesStatus.distService.serviceUrl` will contain the URL of the driver distribution service, which can be used to configure the driver distribution service in the WekaCluster and WekaClient CRs.

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