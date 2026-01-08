### Sign Drives (backends only)

Drive containers will be scheduled on nodes that have available signed drives.

**Note:** This section covers **exclusive drive signing** for single-cluster deployments. For multi-cluster deployments where physical drives are shared between clusters, see [Drive Sharing](drive-sharing.md).

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

When drives are signed they are propogated into node annotations and extended resources
Annotation: `weka.io/weka-drives: '["233447E40E3C","233447E40CFD","233447E40E5A","19231043BD02","23164A23D27F","233447E40D0F"]'`
Extended resource example: `weka.io/drives: "6"`
This way, multiple wekaclusters can share same nodes, as long as they use different set of drives, which they are not selecting themselves, but rather Weka Operator is selecting them based on the signed drives available on the node