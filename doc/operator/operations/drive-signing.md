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
It is also possible to sign drives using WekaManualOperation with signDrivesPayload
In both cases, manual operation and policy,  - spec.image should not be specified as this is not same image as weka containers
The only cases when spec.image might be specified - is when there is specific need to use different signing image, like local distribution or hotfix-version, in such cases it will be instructed specifically
