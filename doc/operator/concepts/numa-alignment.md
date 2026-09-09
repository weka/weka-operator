# NUMA alignment

How WEKA containers are confined to a single NUMA node, what Kubernetes and the host must be
configured to make that confinement hold, and the current limits of NUMA placement in the operator.
How many cores a container gets and which core ids it may use is covered in `cpu-policy.md`; this
document covers where those cores are placed.

## Overview

A WEKA container runs poll threads on exclusive cores and allocates hugepages. Both should come from
one NUMA node, ideally the node local to the container's NICs and NVMe drives. The operator
expresses this as a NUMA request on the pod; kubelet's CPU manager and topology manager decide
whether the request is honoured.

| Capability | Supported |
|------------|-----------|
| Confine a WEKA container to one NUMA node | Yes, `spec.numa` |
| Different NUMA node per container role (drive, compute, S3, NFS, SMB-W, data services) | Yes, `spec.roleNuma` on WekaCluster |
| NUMA confinement for WekaClient | Yes, `spec.numa` on WekaClient |
| Two enforcement methods: kubelet device plugin or Dynamic Resource Allocation (DRA) | Yes, `spec.numa.method` |
| Pin to specific core ids through the NUMA mechanism | No. Use `cpuPolicy: manual` with `coreIds` (`cpu-policy.md`) |
| Two containers of the same role on one node, one per NUMA node | No. The planner places at most one drive and one compute container per node |
| NIC or NVMe selection driven by the NUMA region | No. Drives and network devices are selected independently of `spec.numa` |

## API

```yaml
# WekaCluster
spec:
  numa:                      # default for all backend and protocol containers
    single: true             # confine to one NUMA node
    region: 0                # NUMA node index (0-based); required together with single
    method: device-plugin    # device-plugin (default) | dra
  roleNuma:                  # per-role override, same fields as numa
    drive:
      single: true
      region: 1
    compute:
      single: true
      region: 0
    # also: s3, nfs, smbw, dataServices

# WekaClient
spec:
  numa:
    single: true
    region: 1
```

Rules:

- Confinement is applied only when both `single: true` and `region` are set. `single` alone has no effect.
- `method` omitted means `device-plugin`.
- `roleNuma.<role>` replaces `numa` for that role; roles without an override inherit `numa`.
- NUMA settings apply to compute, drive, S3, NFS, SMB-W and data-services containers only.
  Auxiliary containers (envoy, telemetry, drivers) are not confined.
- Changing `numa` or `roleNuma` on a running WekaCluster, or `numa` on a WekaClient, is propagated
  to the existing WekaContainers.

## Enforcement methods

### `device-plugin` (default)

The node-agent runs a kubelet device plugin. It discovers the node's NUMA nodes from
`/sys/devices/system/node` and advertises each one as an extended resource
`weka.io/numa-region-<N>`. Every advertised device carries kubelet `TopologyInfo` naming its NUMA
node, so kubelet's Device Manager provides a NUMA hint for it and the topology manager can align
the pod's other aligned resources (exclusive CPUs from the static CPU manager) to the same node.

A confined pod requests one unit of `weka.io/numa-region-<region>`. On allocation the device plugin
sets `WEKA_NUMA_REGION=<N>` in the container environment.

Enable it in the operator Helm values:

```yaml
nodeAgent:
  devicePlugin:
    enabled: true
  kubeletPath: /var/lib/kubelet   # change if kubelet is installed elsewhere
```

The device plugin is off by default. With it off, no node advertises `weka.io/numa-region-<N>` and
a pod requesting it cannot be scheduled.

### `dra`

Uses Kubernetes Dynamic Resource Allocation (`resource.k8s.io/v1`, GA since Kubernetes 1.34) with
the upstream `kubernetes-sigs/dra-driver-cpu` driver. For each confined container the operator
creates a ResourceClaim in the container's namespace against the driver's `dra.cpu` DeviceClass,
selecting the device whose `numaNodeID` attribute equals `region` and requesting `dra.cpu/cpu`
capacity equal to the pod's CPU request. The pod references the claim; the driver pins the
container to the allocated CPUs.

Requirements and behaviour:

- `dra-driver-cpu` must be installed. If the `dra.cpu` DeviceClass is missing, the container
  reconcile fails with an explicit error.
- The operator needs RBAC on `deviceclasses` (get, list, watch) and `resourceclaims` (create,
  delete, get, list, watch). The Helm chart grants both.
- Only `cpuPolicy` `dedicated`, `dedicated_ht`, or `auto` (which resolves to one of them) is
  accepted. `manual` and `shared` are rejected, because the claim's CPU count must equal the pod's
  integer CPU request. The driver documents this mirrored CPU request as temporarily required for
  correct scheduler accounting.
- The claim is created before the pod. A ResourceClaim's device request is immutable, so a change of
  `region` or of the container's CPU count is applied by deleting and recreating the claim; a claim
  still reserved by a running pod is left in place until that pod is gone.
- `dra-driver-cpu` states that it and the kubelet CPU manager are mutually incompatible and only one
  can be enabled on a node. Nodes using `method: dra` therefore cannot use the static CPU manager
  configuration described below and rely on the DRA driver for exclusivity.

`method: dra` has not been validated by WEKA for production use. Use `device-plugin`.

## Kubelet configuration

The operator only requests alignment. Kubelet enforces it:

```yaml
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
cpuManagerPolicy: static
reservedSystemCPUs: "<system set>"        # whole physical cores, including SMT siblings
cpuManagerPolicyOptions:
  strict-cpu-reservation: "true"          # optional, see below
topologyManagerPolicy: restricted
topologyManagerPolicyOptions:
  max-allowable-numa-nodes: "<N>"         # only on nodes with more than 8 NUMA nodes
```

- The static policy requires a non-zero CPU reservation. `reservedSystemCPUs` names the exact CPUs.
- Only containers in Guaranteed pods with integer CPU requests receive exclusive CPUs. WEKA
  containers with `cpuPolicy` `dedicated`/`dedicated_ht` (the `auto` default) meet this.
- Changing the CPU manager policy or its options requires draining the node, stopping kubelet,
  removing `/var/lib/kubelet/cpu_manager_state`, then restarting kubelet. Otherwise kubelet
  crashloops on the checkpoint mismatch.
- `strict-cpu-reservation` (GA since Kubernetes 1.35) prevents pods of every QoS class from running
  on the reserved CPUs. It does not influence where exclusive CPUs are placed.
- The topology manager refuses to run on nodes with more than 8 NUMA nodes unless
  `max-allowable-numa-nodes` (GA since Kubernetes 1.35) is raised above the node's count.
- Alignment of hugepages with the same NUMA node requires the kubelet Memory Manager with its
  `Static` policy in addition to the CPU manager.

| Topology manager policy | Effect |
|-------------------------|--------|
| `single-numa-node` | Admits a pod only if all its aligned resources fit one NUMA node; otherwise the pod is rejected at admission |
| `restricted` | Admits only when the hint providers' preferred affinities can be met; otherwise rejected at admission. **Recommended** |
| `best-effort` | Records the preferred affinity but admits the pod even when it cannot be met, so a WEKA pod may land split across nodes |

A pod rejected at admission is left in `Terminated` state and is not rescheduled by the scheduler;
the owning controller must recreate it.

## Host CPU partitioning

The cpusets kubelet applies constrain only the pod's own processes. Kernel threads, interrupt
handlers and kernel workqueues are placed by the kernel and, by default, may run on any CPU,
including a WEKA poll core. Work that lands there competes with a 100%-busy thread.

Split the node into a *system set* (the CPUs in `reservedSystemCPUs`) and a *workload set*
(everything else), and point each kernel control at the system set:

| Control | Mechanism |
|---------|-----------|
| Partition | `cpuManagerPolicy: static` + `reservedSystemCPUs` (above) |
| Steerable interrupts to the system set | `/proc/irq/<N>/smp_affinity_list`, `/proc/irq/default_smp_affinity`, or the `irqaffinity=` kernel parameter |
| Keep irqbalance from moving them back | `IRQBALANCE_BANNED_CPULIST=<workload set>` in the irqbalance environment file, or disable irqbalance |
| Unbound kernel workqueues | `/sys/devices/virtual/workqueue/cpumask` |
| Host daemons started by systemd | `CPUAffinity=<system set>` in `system.conf`, with a per-unit override for the container runtime |
| Block I/O completion work | `rq_affinity=2` in `/sys/block/<dev>/queue/` so completions run on the submitting CPU |

Reserve whole physical cores (a CPU together with its SMT siblings) and size the system set for the
interrupt, workqueue and daemon load it now carries.

## Multiple NUMA nodes

By default the static CPU manager packs a container's exclusive CPUs onto one NUMA node until it is
full and spills the remainder to the next. The `distribute-cpus-across-numa` option spreads CPUs
evenly only when more than one NUMA node is required to satisfy the request; it does not split a
request that fits in one node.

**Recommended: bind by container role.** Give each role its own NUMA node with `roleNuma`, for
example drive containers on NUMA 1, compute containers on NUMA 0 and WekaClient on NUMA 1, with
core counts sized to fit each node. This uses only the built-in kubelet managers.

**Not supported: one cluster split symmetrically across NUMA nodes on the same host.** The planner
places at most one drive container and one compute container per node, so a layout with two
containers of the same role per node, each bound to a different NUMA node with its local NVMe
drives and NICs, is not possible. Drive and NIC selection is also independent of `spec.numa`; only
CPUs (and, with the Memory Manager, hugepages) are aligned.

For explicit per-pod core placement outside these mechanisms, the `weka/nri-cpuset` NRI (Node
Resource Interface) plugin pins annotated pods (`weka.io/core-ids`) to exact CPUs with NUMA memory
binding, gives Guaranteed integer-CPU pods automatic exclusive cores, and runs alongside containerd
1.7+/2.0+. Its README describes it as early-stage and in need of more testing.

## References

- Operator: `pkg/weka-k8s-api/api/v1alpha1/container_types.go` (`WekaNuma`), `wekacluster_types.go` (`RoleNumaSelector`, `GetNumaForRole`), `internal/controllers/resources/pod.go` (pod wiring), `internal/controllers/wekacontainer/funcs_numa_dra.go` (DRA claim), `internal/node_agent/deviceplugin/` (device plugin)
- Kubernetes: [CPU management policies](https://kubernetes.io/docs/tasks/administer-cluster/cpu-management-policies/), [Node resource managers](https://kubernetes.io/docs/concepts/policy/node-resource-managers/), [Topology manager](https://kubernetes.io/docs/tasks/administer-cluster/topology-manager/), [Memory manager](https://kubernetes.io/docs/tasks/administer-cluster/memory-manager/), [Dynamic Resource Allocation](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
- Drivers and plugins: [kubernetes-sigs/dra-driver-cpu](https://github.com/kubernetes-sigs/dra-driver-cpu), [weka/nri-cpuset](https://github.com/weka/nri-cpuset), [containerd/nri](https://github.com/containerd/nri)
- Linux: [kernel parameters](https://www.kernel.org/doc/html/latest/admin-guide/kernel-parameters.html) (`irqaffinity`), [IRQ affinity](https://www.kernel.org/doc/html/latest/core-api/irq/irq-affinity.html), [workqueue](https://www.kernel.org/doc/html/latest/core-api/workqueue.html), [block queue sysfs](https://www.kernel.org/doc/html/latest/block/queue-sysfs.html) (`rq_affinity`), [systemd-system.conf](https://www.freedesktop.org/software/systemd/man/latest/systemd-system.conf.html) (`CPUAffinity`), [irqbalance](https://github.com/Irqbalance/irqbalance)

## Cross-references

- `cpu-policy.md`: `cpuPolicy`, `coreIds`, hyperthreading siblings
- `../deployment/helm-install.md`: `nodeAgent.devicePlugin.enabled`, `nodeAgent.kubeletPath`
- `../deployment/cluster-capacity.md`: planner placement rules
