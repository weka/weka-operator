There is option to use Weka CSI in NFS-only mode, for cases when there is no option to use standard WekaClients
It is also useful when wekacontainer's local data needs to be placed on PVC, this way CSI in NFS mode  provides CSI that can be used for such purposes
PVC used for such purposes should have ReadWriteMany

For such purposes CSI should be installed with such values:
```yaml
pluginConfig:
  allowInsecureHttps: true
  mountProtocol:
    allowNfsFailback: true
    useNfs: true
csiDriverName: nfs-csi.weka.io
logLevel: 6
```

Such CSI should be installed with release-name "weka-nfs" and namespace "weka-nfs", appropriate storage class usually named `weka-nfs-sc`
storage class SHOULD NOT have `mountOptions: forcedirect`

Storage class for CSI should include following, unless specified differently
```
  filesystemName: default
  volumeType: dir/v1
```