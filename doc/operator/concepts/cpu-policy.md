WekaCluster/WekaClient property
- `cpuPolicy` - default set to `auto` , which will automatically discover whether nodes are running with hyperthreading or not, allocating cores accordingly.
    - 2 weka cores == 2 full cores, meaning 5 hyperthreads reserved for a pod. 2 full cores + 1 additional hyperthread for non-dpdk processes, like weka management, telemetry processes, weka agent and so on
    - `coreIds` - used in combination with `cpuPolicy: manual`
    - unless advised by Weka personal, avoid using any other policy rather then default `auto`
    - when specifying manual cores IDs should also include siblings if hyperthreading is enabled, and also keep one additional core(HT or full core, depending on hyperthreading status) free for non-dpdk processes

WekaClient admission (`client_cpu_policy_core_ids`, always rejects; an empty `cpuPolicy` counts as `auto`):
- `manual` requires non-empty `coreIds`; `coreIds` is valid only with `manual`; `nonDatapathCoreIds` is valid only with `manual` or `shared`.
- Update: these rules apply only when the policy changes. Existing objects that already violate them stay editable.
- Core lists are copied to client containers only at creation, and existing containers keep them after the WekaClient's lists are cleared. So an update cannot move a WekaClient into `manual` or `shared` (the policies that apply the lists) or change `coreIds` / `nonDatapathCoreIds`: recreate it. The exception is clearing both lists while moving to `auto`, `dedicated` or `dedicated_ht`.

See `numa-alignment.md` for placing those cores on a NUMA node (`spec.numa`, `spec.roleNuma`) and the kubelet/host configuration required to keep them exclusive.
