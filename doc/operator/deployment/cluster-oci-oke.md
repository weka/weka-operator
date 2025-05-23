# WEKA Cluster Provisioning In OCI (OKE) using the Weka Operator

## OKE cluster Provisioning
Using bliss(tool for provisioning K8s cluster), required flags:
- additional subnet: `--additional-subnet-id`
 
For OKE provisioning driversDistService should be set to https://drivers.weka.io
No need to provision in-cluster driver builder/distribution services

```bash
bliss provision oci-oke \
--oci-subnet-id PROVISIONING_SUBNET \
--additional-subnet-id ADDITIONAL_SUBNET \
--oci-security-groups OCI_SECURITY_GRUOPS \
--compartment-id COMPARTMENT_ID \
--availability-domain AVAILABILITY_DOMAIN \
--source-image-id IMAGE_ID \
--ssh-public-key-path LOCAL_SSH_PUBLIC_KEY_PATH \
--bliss-template-file /path/to/template.yaml --cluster-name OKE_CLUSTER_NAME \
--tag Owner=OWNER --tag TTL=TTL \
--kubeconfig /path/to/kubeconfig-OKE_CLUSTER_NAME 
```
Notes:
- Owner is in format user@domain.example and TTL is in format of duration "3h"
- Common approach is to use same CLUSTER_NAME as for wekacluster for OKE_CLUSTER_NAME, but they dont have to be same
- Bliss will save kubeconfig for cluster into specified path and it should be used on following invocations of kubectl, or any other method of communicating with k8s

Bliss template file describes node groups that will be provisioned and attached to OKE, as following examples:

- Bliss template for converged deployment, i.e clients and backends on the same nodes:
```yaml
nodes:
    - name: Converged
      InstanceType: "VM.Standard.E5.Flex",
      initialSize: 6
      labels:
        key: value
      hugepages: 8000
      Oci: &types.OciConfig{
        NumCores:          10,
        Ram:               80,
        RootDiskSizeGB:    200,
        RootDiskVpusPerGB: 20, // Higher Performance (https://docs.oracle.com/en-us/iaas/Content/Block/Concepts/blockvolumeperformance.htm#vpus)
        DataDevices: []types.OCIDataDevice{
                {
                  DeviceName: "/dev/oracleoci/oraclevdb", // Consistent device path (https://docs.oracle.com/en-us/iaas/Content/Block/References/consistentdevicepaths.htm)
                  SizeGB:     100,
                  VpusPerGB:  20,
                },
        },
      }
cloud: oci
```

If using mix of multiple instance types, use appropriate dataNICsNumber for each instance type/scheduling by appropriate node selectors(and set this labels during provision)

On bliss-provisioned cluster, prior to install of operator - secrets to download from quay.io should be added
This can be done by another bliss command:
It is assumed and expected that environment that executes it has `QUAY_USERNAME` and `QUAY_PASSWORD` env vars, following command will create `quay-io-robot-secret` secret in `default` and `weka-operator-system` namespaces
```bash
bliss k8s ensure-secret quay --cluster-name OKE_CLUSTER_NAME --username $QUAY_USERNAME --password $QUAY_PASSWORD --namespaces default --namespaces weka-operator-system
```

### WekaCluster ports

For OKE provision weka cluster base port needs to be overriden and set as
```
  ports:
    basePort: 15000
```

### Cleanup
To delete cluster provisioned using bliss, use `bliss delete CLUSTER_NAME` command

# WekaCluster/Client Pre-requisites

### Install operator
Before installing weka resources(policies including) operator should be installed, no special instructions for OKE

### Ensure Nics
In OKE setup each weka core (both clients and backends) needs a dedicated NIC(OCI VNIC), total amount of cores cannot be higher then number of available NICs.
Note, that one NIC should be reserved, so considering you have 4 VNICs, you can have 3 data NICs.
For above example of converged configuration, you will need to set dataNICsNumber to 3
```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaPolicy
metadata:
  name: ensure-nics-policy
  namespace: default
spec:
  type: "ensure-nics"
  image: quay.io/weka.io/weka-in-container:4.4.5.118-k8s.4
  imagePullSecret: "quay-io-robot-secret"
  payload:
    ensureNICsPayload:
      type: oci
      dataNICsNumber: 3
```

### Sign Drives (backends only)
Specify "all-not-root", so policy will search for all node NVME devices
```yaml
  payload:
    signDrivesPayload:
      type: "all-not-root"
```

# Commonly used instance types (if not listed, use public OCI documentation)
VM.Standard.E5.Flex supports up to 24 VNICs here are some setup examples:
- VM.Standard.E5.Flex - 3 data NICs, 2 drives, 3 available for weka cores
- VM.Standard.E5.Flex - 7 data NICs, 2 drives, 7 available for weka cores
