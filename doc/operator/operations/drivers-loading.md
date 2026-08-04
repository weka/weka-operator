# Drivers Loading

Weka containers that run the datapath need the Weka kernel modules loaded on their host
node before they can start. The operator loads these modules through a dedicated,
short-lived **drivers-loader** container and records the result on the node so that other
containers on the same node reuse it.

For how the driver *files* are produced and distributed (the drivers-builder /
drivers-dist services), see [Driver Distribution](../deployment/driver-distribution.md).

## One loaded driver version per node

A node has **exactly one loaded driver version at a time** — the kernel cannot hold two
Weka driver versions at once, and all containers on the node share it.

The loader is a single, owner-less `WekaContainer` per node, named
`weka-drivers-loader-<nodeUID>` in mode `drivers-loader`
(`internal/controllers/operations/load_drivers.go`). Because it is shared and owner-less,
concurrent reconciles on the same node may all reach for it at once; the priority model
below keeps that race convergent instead of a delete/recreate loop.

### Node annotation: `weka.io/drivers-loaded`

The result of a successful load is recorded on the node in the `weka.io/drivers-loaded`
annotation as a single JSON record with three fields:

- `boot_id` — the node's BootID, tying the record to the current boot. Kernel modules do
  not survive a reboot, so on a boot-id change the record is stale and cleared, and the next
  reconcile reloads.
- `image` — the container image whose driver files were loaded.
- `priority` — the rank of the container that won the load (see
  [Priority model](#priority-model)).

A legacy `image:bootId` string form is still parsed for back-compat and treated as
priority `0` (`parseLoadedDrivers`).

### Loader label: `weka.io/driver-priority`

While a load is **in flight**, the record does not exist yet, but a concurrent reconcile
still needs to order itself against the running loader. The loader's version comes from its
`Spec.Image`, but its priority does not — so it is stamped on the loader as the
`weka.io/driver-priority` label, then read back to decide whether to defer to or preempt
the in-flight load, and to record the winning priority in the annotation.

### Loader label: `weka.io/driver-boot-id`

The loader is also stamped with the node BootID it was created for. Kernel modules do not
survive a reboot, but a loader `WekaContainer` and its persisted `ExecutionResult` do — so
a loader left over from a previous boot carries a *pre-reboot* success record. Reusing it
would let the poll/record path mark drivers as loaded for the current boot without an
actual reload.

The stamp lets any reconcile tell such a stale loader apart from one freshly created for
the current boot and delete it instead of reusing it. This check runs before an existing
loader is ever polled, whenever a loader is present — *not* behind reboot detection, which
is gated on the discovery annotation and is missed when it is absent — so a stale loader is
never polled regardless of discovery state. A loader with no stamp (legacy, pre–boot-id)
never matches a live BootID and is treated as stale.

## Priority model

Selection across containers competing for a node is a **total order**: first by priority,
then by Weka version. The node loads the **maximum**. Because movement is only ever toward
a higher priority or a newer version, the process is monotonic and converges without
ping-pong.

Priority comes from the container's role (`driverPriority` in
`internal/controllers/wekacontainer/funcs_drivers.go`):

| Priority | Containers | Strictness |
|----------|-----------|------------|
| 3 | Frontends (`HasFrontend()`: `client`, and `s3` / `nfs` / `smbw` / `data-services` with frontend cores) | **Strict** |
| 2 | Backend-only (`drive`, `compute`, backend-only `data-services`) | **Lenient** |
| 1 | `ssdproxy` | **Lenient** |

Strictness determines when a container is satisfied by whatever is already loaded:

- **Frontends are strict** — satisfied only by their *exact* version. A frontend's version
  is bounded by the cluster it joins and it cannot run on a mismatched driver, so it
  **dictates** the node's loaded version. Hence the highest priority.
- **Backends and ssdproxy are lenient** — satisfied by *any* valid loaded version. They
  never force a version and never conflict.

Ordering is done by `compareDriverOrder`: it compares priority first, and only falls back
to version (`utils.CompareVersions` over `utils.GetSoftwareVersion(image)`) when priorities
are equal.

## Decision logic

Before touching the loader, `EnsureDrivers` computes the container's effective image and
asks `EvaluateDrivers(node, image, priority, isFrontend)` what to do — a pure function of the
node annotation.

That annotation is **a record, not a lock**: nothing clears it except a node reboot or a
successful load, so it outlives the container that wrote it. A record that outranks us is
therefore only honored while some *live* container on the node still demands that version. On
`DriverConflict`, `EnsureDrivers` lists the node's other *frontend* containers
(`nodeFrontendDemands`) and calls `BlockingPeer` to check; if none outranks us the record is
orphaned and we preempt it. Frontends are the whole list because only a frontend can outrank a
frontend, and only a frontend reaches `DriverConflict`.

| Decision | When | Action in `EnsureDrivers` |
|----------|------|---------------------------|
| `DriverLoad` | Nothing loaded, boot-id mismatch, or a strict frontend out-orders what is loaded | Set status *waiting for drivers*, run the loader |
| `DriverSatisfied` | Loaded image == my image | Proceed (return nil) |
| `DriverDefer` | Lenient container (backend / ssdproxy) and *some* valid version is already loaded | Proceed silently (return nil) |
| `DriverConflict` | Strict frontend outranked by the record, **and** `BlockingPeer` names a live peer that still demands it | `DriversVersionConflict` warning naming that peer, stay waiting |
| `DriverConflict` → `DriverLoad` | No live peer demands the record — it is orphaned | `DriversPreemptStaleRecord` (Normal), then load, preempting the record |
| *(preempt deferred)* | Record is orphaned, but a non-terminal *driver-consuming* pod on the node still runs the recorded image | `DriversWaitForConsumer` warning, requeue 30s |

Because the conflict is decided against live demand, it clears on the next reconcile once the
blocking peer leaves the node, rather than needing a reboot.

Only a frontend reaches `DriverConflict`, so only another frontend can outrank it — and pod
anti-affinity (over `domain.ContainerModesWithFrontend`) normally keeps two frontends off one
node. On such a node `BlockingPeer` always comes back empty, the `DriversVersionConflict` row is
unreachable, and the last row is the outcome an operator actually sees (hence its event names
both images). `BlockingPeer` is load-bearing only where two frontends can share a node:
`ALLOW_MULTIPLE_PROTOCOLS_PER_NODE`, `Spec.NoAffinityConstraints`, or the scheduling races noted
on `getFrontendPodsOnNode`.

`HasFrontend()` is a superset of that list on paper — `data-services` with
`dataServicesFeCores > 0` counts as a frontend for priority but is not selected on by the
anti-affinity. That field is not used in practice today, so the two coincide; if it ever is, a
`data-services` container becomes a frontend the anti-affinity cannot see.

Digest-pinned images (`repo@sha256:…`) never reach any of this: `GetSoftwareVersion` yields no
version from a digest, so two such images compare equal and `DriverDefer` wins at order `0`.

The load itself runs in `LoadDrivers.GetSteps()` (`load_drivers.go`): create the loader
(stamping the `weka.io/driver-priority` label), poll it, and on success write the
`{boot_id, image, priority}` record to the node annotation, then delete the loader pod. If
a loader already exists, the container first discards it when its boot-id stamp is stale
(see [`weka.io/driver-boot-id`](#loader-label-wekaiodriver-boot-id)), then polls it when the
image matches, deletes and replaces it when it out-orders the in-flight load, or defers
otherwise — this is the anti-race core. When preempting an orphaned record, an in-flight
loader for a different image is discarded too, for the same reason. The loader reports success
by writing the loaded driver name to `/tmp/weka-drivers.log`
(`charts/weka-operator/resources/weka_runtime.py`), read back via `CheckDriversLoaded`
(`funcs_drivers.go`). Because the `rmmod` of the previous driver is best-effort, the loader
also runs `weka driver ready --version {version}` before reporting success, so a successful
load means the *requested* version is genuinely resident.

### Node reboot

Kernel modules do not persist across a reboot. When the node's BootID differs from the
recorded one, the operator clears `weka.io/drivers-loaded`, so the next reconcile sees
"nothing loaded" and reloads.

### Force reload

`funcs_weka_local_status.go` triggers a **forced** load (`force=true`) when the container's
local status cannot be read and drivers are not confirmed loaded, bypassing the "already
loaded for this boot" short-circuit and reloading regardless.

## Loader image selection

The image used *for the loader pod* is not always the container's own image
(`GetLoaderImageForNode`, `internal/drivers/drivers.go`):

- If the target image's feature flags report `WekaGetCopyLocalDriverFiles`, the loader runs
  the cluster image directly.
- Otherwise the loader runs the **builder image** for the node and the driver files are
  copied in via `InstructionCopyWekaFilesToDriverLoader`.

## Limitations

- **Preempting an orphaned record waits for any driver-consuming pod still running the
  recorded image.** A lower-version frontend only ever arrives on a node as a newly created
  container (existing containers are never downgraded). If it lands on a node whose record was
  left by a higher-version container, it preempts that record — but not while a non-terminal
  pod is still running the recorded image (`DriversWaitForConsumer`, 30s requeue), since
  unloading under it would fail on remnant mounts. Only pods whose mode actually consumes
  drivers count (`RequiresDrivers()`, evaluated from the pod's `weka.io/mode` label): the
  drivers-loader pod itself runs that very image, and `dist` / `drivers-builder` / `envoy` /
  `telemetry` pods are long-lived on it, yet none hold wekafs mounts — counting them would make
  preemption permanently unreachable. So a node still hosting a live backend cluster on the
  newer image stays blocked (correctly), while a node whose cluster is gone unblocks.
- **A backend image upgrade does not by itself reload the node driver.** Backends are
  lenient by design and tolerate the loaded version; this preserves existing behavior. A
  frontend upgrade (or a reboot) is what moves the node's loaded version forward.

## See also

- [Driver Distribution](../deployment/driver-distribution.md) — how driver files are built
  and served.
- [Drive Signing](drive-signing.md) — a separate BootID-gated, per-node operation for
  backend drives.
