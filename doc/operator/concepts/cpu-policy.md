WekaCluster/WekaClient property
- `cpuPolicy` - default set to `auto` , which will automatically discover whether nodes are running with hyperthreading or not, allocating cores accordingly.
    - 2 weka cores == 2 full cores, meaning 5 hyperthreads reserved for a pod. 2 full cores + 1 additional hyperthread for non-dpdk processes, like weka management, telemetry processes, weka agent and so on
    - `coreIds` - used in combination with `cpuPolicy: manual`
    - unless advised by Weka personal, avoid using any other policy rather then default `auto`
    - when specifying manual cores IDs should also include siblings if hyperthreading is enabled, and also keep one additional core(HT or full core, depending on hyperthreading status) free for non-dpdk processes