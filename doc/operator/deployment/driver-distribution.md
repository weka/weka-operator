Driver distribution is usually a part of operator install, i.e no need to install it unless also installing operator

Example of driver distribution service:

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
  image: quay.io/weka.io/weka-in-container:4.4.5.118-k8s.4
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
  name: weka-drivers-builder-mtu-118-4
  namespace: weka-operator-system
spec:
  agentPort: 60001
  image: quay.io/weka.io/weka-in-container:4.4.5.118-k8s.4
  imagePullSecret: "quay-io-robot-secret"
  mode: "drivers-builder"
  name: dist 
  numCores: 1
  uploadResultsTo: "weka-drivers-dist"
  port: 60002
  overrides:
    preRunScript: "apt-get update && apt-get install -y gcc-12"
```

Builder container must match version of wekacluster/wekaclient that is going to be provisioned later
One builder should be deployed per used weka version
Dist has wide back and forward compatibility range. Best to use wekacluster/wekaclient version if known, if not known, use the one in example
wekacontainer can have explicit nodeAffinity, or nodeSelector if it matters where to schedule. It usually matters if there is are multiple kernels, and such usually will be noticed explicitly