### Sign Drives (backends only)
Drive containers will be scheduled on nodes that have available signed drives
To scan nodes for drives that can be used for weka and sign them, apply following policy

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaPolicy
metadata:
  name: sign-drives
  namespace: weka-operator-system # Replace with your namespace
spec:
  type: sign-drives
  payload:
    signDrivesPayload:
      type: "all-not-root"
```
