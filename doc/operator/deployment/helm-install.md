Installed with command as

```bash
helm upgrade --create-namespace \
    --install weka-operator oci://quay.io/weka.io/helm/weka-operator \
    --namespace weka-operator-system \
    --version v1.5.0-dev.44 --values operator_values.yaml
```

Operator pod can be found with label app=weka-operator after installation

notable helm-level values:

`localDataPvc` - global PVC to use for container's local data
Helm-level configure of PVC is deprecated and should be used only when explicitly instructed
If just asked to "use pvc for cluster", use cluster spec field instead. `globalPVC` object field on cluster spec with, with `name` field on it, which references PVC
