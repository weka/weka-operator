Driver distribution is usually a part of operator install, i.e no need to install it unless also installing operator

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
    driverDistPayload:
      ensureImages:
        - "quay.io/weka.io/weka-in-container:4.4.2.157-k8s.2" # Example Weka image version for proactive building
        - "quay.io/weka.io/weka-in-container:4.4.5.118-k8s.4" # Another example
      # NodeSelectors to define which nodes this policy applies to for driver building.
      # Builders will be scheduled on nodes matching these selectors AND discovered kernel/arch.
      nodeSelectors:
        - role: "worker-nodes"
          environment: "production"
        - custom-label: "drivers-build-pool"
        - {}
      # Optional: Custom labels for kernel and architecture. Defaults to weka.io/kernel and weka.io/architecture
      # kernelLabelKey: "custom.io/kernel-version"
      # architectureLabelKey: "custom.io/arch"
      # Optional: A script to run on builder containers after kernel validation but before the main build process
      builderPreRunScript: |
        #!/bin/sh
        apt-get update && apt-get install -y gcc-12
```

After deploy of policy `status.typesStatus.distService.serviceUrl` will contain the URL of the driver distribution service, which can be used to configure the driver distribution service in the WekaCluster and WekaClient CRs.