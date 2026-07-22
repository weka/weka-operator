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
asks `EvaluateDrivers(node, image, priority, isFrontend)` what to do. The decision is a
pure function of the node annotation:

| Decision | When | Action in `EnsureDrivers` |
|----------|------|---------------------------|
| `DriverLoad` | Nothing loaded, boot-id mismatch, or a strict frontend out-orders what is loaded | Set status *waiting for drivers*, run the loader |
| `DriverSatisfied` | Loaded image == my image | Proceed (return nil) |
| `DriverDefer` | Lenient container (backend / ssdproxy) and *some* valid version is already loaded | Proceed silently (return nil) |
| `DriverConflict` | Strict frontend, but a version of **equal-or-higher order** is already loaded and it isn't mine | Emit a `DriversVersionConflict` warning, stay waiting |

A `DriversVersionConflict` warning (throttled) fires only on a genuine conflict: two strict
frontends on one node whose versions cannot both be satisfied. The lower-order frontend
stays pending — it cannot run on the wrong driver — until the situation resolves (a reboot,
or the other frontend leaving the node).

The load itself runs in `LoadDrivers.GetSteps()` (`load_drivers.go`): create the loader
(stamping the `weka.io/driver-priority` label), poll it, and on success write the
`{boot_id, image, priority}` record to the node annotation, then delete the loader pod. If
a loader already exists, the container first discards it when its boot-id stamp is stale
(see [`weka.io/driver-boot-id`](#loader-label-wekaiodriver-boot-id)), then polls it when the
image matches, deletes and replaces it when it out-orders the in-flight load, or defers
otherwise — this is the anti-race core. The loader reports success by writing the loaded driver name to
`/tmp/weka-drivers.log` (`charts/weka-operator/resources/weka_runtime.py`), read back via
`CheckDriversLoaded` (`funcs_drivers.go`).

### Node reboot

Kernel modules do not persist across a reboot. When the node's BootID differs from the
recorded one, the operator clears `weka.io/drivers-loaded`, so the next reconcile sees
"nothing loaded" and reloads. Reboot is also the point at which a stuck configuration (e.g.
an in-place downgrade, see [Limitations](#limitations)) naturally resets.

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

- **In-place frontend downgrade (V2 → V1, no reboot)** does not auto-apply. Selection is
  monotonic and will not move to a lower version; it resolves at the next node reboot,
  which clears the annotation.
- **A backend image upgrade does not by itself reload the node driver.** Backends are
  lenient by design and tolerate the loaded version; this preserves existing behavior. A
  frontend upgrade (or a reboot) is what moves the node's loaded version forward.

## See also

- [Driver Distribution](../deployment/driver-distribution.md) — how driver files are built
  and served.
- [Drive Signing](drive-signing.md) — a separate BootID-gated, per-node operation for
  backend drives.
