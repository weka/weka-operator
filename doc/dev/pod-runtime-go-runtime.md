# Pod-runtime Go architecture

The pod-runtime is the process that runs inside every Weka container pod,
replacing `charts/weka-operator/resources/weka_runtime.py`. Entry point:
`cmd/weka-pod-runtime/main.go`. Code: `internal/runtime/`.

> **Status as of 2026-09-21**: every package described below, including the
> mode-runtime/factory layer in "Runtime families" and the coordinator in
> "Coordinator state machine and context tree", is landed and committed on
> this branch stack. `main.go`'s flow: `config.CaptureEnv()` →
> `config.ParseRuntimeSection()` → build a logging-only root `context`
> (never cancelled; the coordinator installs its own signal handling) →
> OTel setup → `process.NewManager(root)` → `coord := lifecycle.New(root, pm)`
> → `runtimes.Deps{Runner, Processes, Clock, Paths, Coord}` (`Processes` is the
> `process.Launcher` interface, satisfied by `*process.Manager`) →
> `runtimes.New(env, deps)` → `coord.Go("logrotate", ...)` when
> `logrotate.Applies` → `coord.Run(root, rt)` → flush OTel →
> `debugexit.Wait` → `os.Exit(outcome.ExitCode())`. No `signal.NotifyContext`
> in `main.go`; the coordinator owns signal installation.
> Build, vet, race tests, lint, and Linux-pod unit test runs pass; E2E is
> pending. See `.plans/progress/runtime-refactor-progress.md` for details.

## Runtime families

Every mode is one of a small set of families, each implementing the shared
contract in `internal/runtime/lifecycle/coordinator.go`:

```go
type ModeRuntime interface {
    Start(context.Context) error
    Shutdown(context.Context) error
}
```

Start and Shutdown never run concurrently for the same runtime.

| Modes | Family | Issues `weka local stop`? |
|---|---|---|
| compute, drive, s3, nfs, smbw | Backend | yes, approval-gated |
| data-services | Backend | yes, forced, no approval |
| client | Client | yes, approval-gated, forced |
| envoy, telemetry | Auxiliary | yes, forced, no approval |
| ssdproxy | SSD proxy | yes, forced, no approval |
| drivers-dist | Driver distribution | yes, forced, no approval |
| adhoc-op-with-container | Container operation | yes, forced, no approval |
| adhoc-op | Task (host ad-hoc workflow) | no |
| drivers-builder | Task (driver builder workflow) | no |
| discovery, drivers-loader | Discovery/driver loader | no |

A `runtimes.Deps` struct and `runtimes.New(env config.Env, deps Deps) (lifecycle.ModeRuntime, error)`
factory (`factory.go`) select the family for the configured mode.

## Startup order

Two shared preparation stages run before any family-specific logic:

1. **Persistent prep** — for Backend, Client, Auxiliary, SSD proxy, and
   Driver distribution only: bind-mount setup
   (`internal/runtime/persistency`), generation lock acquisition
   (`internal/runtime/generation`), management-IP/NIC reconciliation
   (`internal/runtime/network`), and — when
   `domain.NeedsOperatorResources(mode)` is true — the wait for
   `resources.json` from the operator (`internal/runtime/resources`).
2. **Agent-backed prep** — for modes that need the weka-agent process:
   `runtimes.startAgent` (driver loading via `internal/runtime/drivers` when
   requested, agent launch, and readiness wait via `internal/runtime/agent`),
   then `runtimes.readFeatures` to read the release spec's feature flags.
   Each family then selects its own Weka version from those flags — see
   "Intentional differences from Python" for SSD proxy's IOMMU check before
   its forced version selection.

CPU affinity (`internal/runtime/cpuaffinity`) and COS hugepages
(`internal/runtime/cos`) are not part of either shared stage: CPU affinity
runs per-mode, after the container starts, in backend and client only; COS
hugepages runs only in discovery. Drive discovery/signing
(`internal/runtime/wekadrive`) is likewise per-mode, not shared: backend
runs it after container start, for the drive mode only.

After both stages, the family's `Start` runs: for Backend/Client this means
`internal/runtime/weka.EnsureContainer` (create or reconcile the Weka
container); for Auxiliary/SSD proxy/Driver distribution/Container operation
it means launching that family's specific process; Adhoc/Discovery/Driver
loader run a single host operation and exit without holding any container.

## Adhoc, drive and NixOS operations

- **`kernelize`** (`internal/runtime/adhoc/kernelize.go`, dispatched at
  `internal/runtime/runtimes/adhoc.go:30`): runs `/weka-sign-drive kernelize -J` to rebind
  NVMe devices left on a userspace driver and writes the per-device result. The operator
  runs it as an `adhoc-op` with `HostPID` (`internal/controllers/wekacontainer/funcs_proxy.go`,
  `runKernelizeBeforeProxyPod`); SSD proxy startup does not kernelize.
- **sign-drives selection** (`internal/runtime/adhoc/sign_drives.go`): payload type
  `device-serials` resolves each serial through `blockdev.GetDevicePathBySerial` and fails if
  any serial has no matching block device. `driveExclusions` rules (model, capacity, type) are
  matched against `weka-sign-drive list` output by `wekadrive.ExcludedPathsByRules`
  (`internal/runtime/wekadrive/sign.go`) and merged with the serial-based exclusions.
- **Discovery of proxy-signed drives** (`internal/runtime/wekadrive/discover.go`): with
  `useSignTool` set, partitions on drives signed for ssdproxy (`ProxySignedPaths`) are skipped.
- **NixOS**: `internal/pkg/osinfo` detects NixOS (`IsNixos`), reports the OS as
  `nixos-gcc<major>` from `/proc/version` (`KernelGccMajor`), and prefixes host `nsenter`
  commands with `/usr/bin/env PATH=...` (`HostNsenterArgs`). `drivers.PrepareNixosHostKernel`
  (`internal/runtime/drivers/nixos.go`) links host kernel headers and modules into the builder
  image; it runs before the pre-run script in the drivers-builder
  (`internal/runtime/runtimes/builder.go:43`) and drivers-loader
  (`internal/runtime/runtimes/driverloader.go:32`) runtimes. `drivers.KernelBuildID` returns the
  OS build ID on NixOS. Discovery results include `proc_version`
  (`internal/runtime/runtimes/discovery.go`).

## Coordinator state machine and context tree

`internal/runtime/lifecycle.Coordinator` (`coordinator.go`) owns four
contexts derived from one root, all cancelled in `Run`'s deferred cleanup:

- `startupCtx` — cancelled the moment a shutdown is requested, so a blocked
  `Start` unblocks immediately.
- `tasksCtx` — background tasks registered via `Go`; outlives startup/shutdown.
  Periodic tasks (`GoPeriodic`: CPU affinity, logrotate) sleep on a child context
  cancelled by the shutdown request, so no new iteration starts once shutdown is
  requested; an iteration already running finishes under `tasksCtx`.
- `servicesCtx` — supervised daemons (`process.Manager`); outlives Weka shutdown.
- `shutdownCtx` — the family's `Shutdown` runs under this.

`Run` drives the state machine:

1. Install SIGTERM/SIGINT handlers before `Start`, so a signal during
   startup is recorded rather than delivered with the default action.
2. Call `rt.Start(startupCtx)`.
3. Branch on outcome:
   - **Shutdown requested** (signal or generation takeover, tracked via
     `RequestShutdown(Reason)`) wins over an overlapping startup error — the
     error is kept for reporting, but `rt.Shutdown(shutdownCtx)` still runs.
   - **Startup error with no request recorded** commits to failure cleanup:
     local cleanup only, no operator-approval wait, no additional
     `weka local stop`. Coordinated rollback after a partial startup failure
     needs a separate operator/runtime design (open item, not yet solved).
   - **Startup succeeded**: block on either a shutdown request (run
     `Shutdown`) or a fatal owned-task error from `Go` (treated as a startup
     failure).
4. Stop signal handling, then run the four-phase cleanup (`cleanup()`):
   1. Cancel `tasksCtx` and join every task registered via `Go`.
   2. `process.Manager.DisableRestarts()` then `Shutdown(shutdownCtx)` —
      terminate every managed process group in reverse registration order
      (see "Process ownership" below), then cancel `servicesCtx`.
   3. Release resources registered via `Register`, in reverse acquisition
      order.
   4. Release the resource registered via `RegisterLast` (the generation
      lock) — always last, so takeover detection stays valid until every
      other resource is gone.

`Outcome.ExitCode()` returns 1 for a genuine `StartupErr`, 0 if the only
`StartupErr` is `context.Canceled` caused by a recorded shutdown request.

## Process ownership and reaping

`internal/runtime/process.Manager` (`manager.go`) owns every subprocess:

- `Run(ctx, Command) (Result, error)` — launch, wait, capture output;
  ctx cancellation sends SIGTERM to the process group and re-uses the same
  `Wait()`, never re-waiting.
- `StartDaemon(ctx, name, Command)` — supervised, relaunched on a fixed
  3-second cadence for as long as restarts are enabled
  (`DisableRestarts()`), reusing the same registration slot across restarts
  so shutdown order stays stable.
- `StartProcess(ctx, name, Command)` — launched once, not restarted.

Every child gets its own process group (`Setpgid: true`); `Manager.Shutdown`
terminates process groups in reverse registration order via SIGTERM only —
it never escalates to SIGKILL, so a process that ignores SIGTERM is left
running (tracked as a TODO on `Manager.Shutdown`).

`internal/runtime/process/reaper_linux.go` scans `/proc` for orphaned
children not in the Manager's registry and reaps them; `reaper_other.go` is
a no-op on non-Linux platforms.

## Shutdown policy

`internal/runtime/shutdown/policy.go`'s `StopPolicy(mode string) (approval, force bool)`
is the single encoding of the approval table:

| Modes | Approval required | First attempt forced |
|---|---|---|
| compute, drive, s3, nfs, smbw | yes | no |
| client | yes | yes |
| data-services, envoy, telemetry, ssdproxy, drivers-dist, adhoc-op-with-container | no | yes |
| everything else (discovery, drivers-loader, drivers-builder, adhoc-op) | no | no — never issues `weka local stop` |

`internal/runtime/shutdown/instructions.go`, `stop.go`, and `drives.go`
implement, respectively: reading the operator-written
`domain.ShutdownInstructions`, the approval-wait/stop-loop/force-escalation
sequence, and waiting for drive release before the container is torn down.
`internal/runtime/runtimes` (`shutdown.go`) calls `StopPolicy()`/`StopLoop()`
directly; the old pre-refactor shutdown API (`shutdown.go`/`legacy.go`) is
gone.

`runtimes.runWekaShutdown` (`shutdown.go`) runs two background workers alongside
the approval wait and stop loop, gated on the family's `agentLaunched` flag only
(never also on the lock, since container operation launches an agent without
ever acquiring the generation lock): a takeover force worker that force-stops
the moment `Coordinator.TakeoverDone()` closes, independent of the approval
wait; and a force watcher (`shutdown.WatchForce`) that races the graceful
`shutdown.StopLoop`, escalating to a forced stop if the operator writes a
force-stop instruction mid-shutdown — started only when the mode's initial
attempt isn't already forced.

`internal/pkg/domain/runtime_policy.go` holds the two pieces of policy shared
between the operator and the runtime: `NeedsOperatorResources(mode string) bool`
(does this mode wait for `resources.json`?) and the `ShutdownInstructions`
struct itself.

## Intentional differences from Python

Per spec §7, these are deliberate, not oversights:

- SIGTERM-only process termination (Python's supervisor could escalate to
  SIGKILL in some paths); see the TODO on `process.Manager.Shutdown`.
- The generation lock is always released last in cleanup, never interleaved
  with other resource releases, even when Python's teardown order varied.
- A startup failure with no shutdown request recorded skips the
  operator-approval wait and any additional `weka local stop` entirely —
  Python attempted best-effort container stop on some startup failure paths.
- Config is captured once as an immutable `config.Env` snapshot
  (`internal/runtime/config/env.go`) rather than read from `os.Environ()`
  ad hoc throughout the process.
- drivers-dist and adhoc-op-with-container shut down by checking their fixed
  stem container name ("dist" / "adhoc"), not the pod identity name — the
  weka container is always created under that fixed name regardless of the
  pod/identity name, and `shutdown.isContainerRunning` filters `weka local
  ps` by it.
- adhoc-op skips persistent and agent preparation (bind mounts, generation, management IPs,
  dependencies flag); Python ran them although no adhoc operation reads their results.
- SSD proxy fails on an empty `MEMORY` before persistent preparation, rather
  than after taking the generation lock and starting the agent.
- The driver builder binds its HTTP listener before publishing success results
  and registers the serving task after publication; connections arriving in
  between queue in the listen backlog. An unexpected server failure is fatal.
- Backend modes share one `Start` with explicit `mode` branches for the
  compute-only telemetry override, compute/drive CPU affinity, and drive-only
  drive setup; data-services differs only through container creation flags and
  its stop policy.
- Sign-drives `device-serials` resolution returns an error when a serial has no matching
  block device (Python returns `None`); a device whose serial cannot be read is treated as a
  non-match while scanning. `kernelize` likewise reports failures as Go errors.
- Discovery's `proc_version` is best-effort: empty when `/proc/version` cannot be read (Python
  raises).
- `PrepareNixosHostKernel` sets `PATH` with `os.Setenv`, which is process-global; it runs only
  on the NixOS builder/loader path.
- Drive maps are iterated in Go map order, so only log order differs from Python.

Unresolved differences and related findings are tracked in
[pod-runtime-parity-findings.md](pod-runtime-parity-findings.md).

## Required TODOs (retained limitations)

- `process.Manager.Shutdown` can block indefinitely on a process that
  ignores SIGTERM (no SIGKILL escalation is implemented).
- Coordinated rollback after a partial startup failure (stopping a Weka
  container that was created but not confirmed healthy) needs a separate
  operator/runtime design; the coordinator currently does local cleanup only
  in that case.
- Real-system validation is still required for Weka/kernel-specific
  behavior (driver loading, NIC reconciliation, CPU affinity, drive
  signing) — current test coverage is synthetic fixtures only, see
  `internal/runtime/testdata/README.md` and
  `doc/dev/pod-runtime-fixture-capture.md` for how to add real-cluster
  captures.

## Pending validation

Unit tests run against synthetic fixtures in `internal/runtime/testdata/`.
Local build/vet/race tests/lint and Linux-pod unit test runs are done; E2E
validation against a real cluster is pending. Details in
`.plans/progress/runtime-refactor-progress.md`.
