Installed with command as

```bash
helm upgrade --create-namespace \
    --install weka-operator oci://quay.io/weka.io/helm/weka-operator \
    --namespace weka-operator-system \
    --version v1.5.0-dev.44 --values operator_values.yaml
```

notable values:

`localDataPvc` - global PVC to use for container's local data

Operator pod can be found with label app=weka-operator