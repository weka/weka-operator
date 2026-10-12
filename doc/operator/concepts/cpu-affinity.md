# CPU affinity inside WEKA pods

How the pod runtime keeps everything that is not a WEKA I/O thread off the I/O cores of its pod.
Which cores a container gets is covered in `cpu-policy.md` and where they sit in `numa-alignment.md`;
this document covers how the processes inside the pod are spread over those cores.

## Overview

A WEKA container's I/O wekanodes poll on dedicated cores. Everything else in the pod — the runtime
itself, the WEKA agent, syslog, the management (slot 0) wekanode, helper threads and commands exec'd
into the pod — is pinned to the remaining cores of the pod, the **support mask**, so it does not steal
time from the poll threads.

| Mechanism | When | Covers |
|---|---|---|
| Early pin | first step of the runtime's `main()` | the runtime and everything it spawns: syslog, driver work, the agent |
| Agent `taskset` | every agent start, including restarts | the agent and the wekanodes it spawns |
| `/root/.bashrc` | interactive `bash` exec'd into the pod | the shell and its children |
| `BASH_ENV` | non-interactive `bash -c` exec'd into the pod | the shell and its children |
| Periodic sweep | right after the container is up, then every 60 seconds | any non-I/O process missed above |

## Which pods

Modes whose WEKA container owns cores: drive, compute, client, s3, nfs, smbw and data-services. The
runtime keys this off `MODE_CORES_FLAG`; the operator off `HasWekaCoresMode`, which also gates the
`BASH_ENV` env var on the pod. Other modes run no I/O cores and get none of this.

Pods with `hostPID: true` are skipped entirely: there `/proc` lists host and other tenants' processes,
so the runtime takes no affinity action at all.

## The support mask

```
support mask = pod cpuset
             − find_full_cores(CORES) and their SMT siblings
             ∩ NON_DATAPATH_CORE_IDS          # only when set explicitly
```

- The pod cpuset is read from PID 1 once and cached before the runtime pins itself, so later
  computations do not see the narrowed mask. It assumes the pod's CPU allocation does not change for
  the lifetime of the runtime.
- The I/O cores come from the same `find_full_cores` call that picks the cores WEKA is configured with,
  not from the masks of live wekanodes: a wekanode starts with the mask it inherits from the agent and
  only later is pinned by WEKA, so reading it would reserve the support cores themselves.
- The mask is published to `/tmp/weka-k8s-runtime/support_cpus`, in the container's own filesystem so
  that a mask from a previous boot with a different cpuset cannot survive a restart.

## Startup

The runtime computes the support mask, publishes it and pins all of its own threads before it starts
syslog, loads drivers or launches the agent, so all of them inherit the mask instead of being corrected
later. The agent command is wrapped in `taskset -c <support mask>`, which keeps every agent restart
pinned as well.

This step is best-effort: if it fails, a warning is logged, the runtime and agent start unpinned and
the periodic sweep corrects them once the container is up.

## Exec'd processes

A process exec'd into the pod starts on the pod's full cpuset, I/O cores included, because it does
not descend from the runtime.

- Interactive `bash`: `/root/.bashrc` sources `/tmp/weka-k8s-runtime/exec_pin.sh`, which pins the
  shell to the published mask.
- Non-interactive `bash -c` / `bash -ce`, which is how the operator execs: the pod's `BASH_ENV` points
  at the same script.
- Not covered: `sh -c` and direct execs without a shell. They run on the full cpuset until the next
  sweep moves them, if they still run by then. Execs made before the runtime has published the mask
  are likewise unpinned.

## Periodic sweep

Every 60 seconds the sweep pins every thread of every non-I/O process in the pod to the support mask.

- I/O wekanodes (`--slot` other than 0) are never touched; WEKA pins their I/O threads itself.
- When the WEKA image advertises feature flag `weka_manages_non_ionode_affinity` (bit 12), WEKA also
  places its own non-I/O threads, from `non_datapath_cores` in the container resources, and the sweep
  skips every wekanode. Without the flag the sweep pins the slot 0 wekanode itself.
- An empty support mask skips the sweep with a warning.

## Observed behaviour

On a fresh boot the I/O leader thread of each wekanode starts on the support mask it inherits and
stays there until WEKA pins it to its I/O core: about 35–45 seconds on 5.1.1.41 and about 4–5 seconds
on 5.1.34. Without `weka_manages_non_ionode_affinity`, the helper threads of I/O wekanodes keep the
inherited support mask and the DPDK interrupt thread may run on any core but its own.

## Related

- `cpu-policy.md` — how many cores a container gets
- `numa-alignment.md` — which NUMA node they come from
