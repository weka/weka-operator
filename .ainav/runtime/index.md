# Pod-runtime navigation

Go rewrite of `weka_runtime.py`. Entry point `cmd/weka-pod-runtime/main.go`.
Details: `doc/dev/pod-runtime-go-runtime.md`.

Flow: `CaptureEnv()` → `runtimes.New()` → `coord.Run()`.

## Core packages

| Path | Purpose | Key entry points |
|---|---|---|
| `config/` | Env capture + immutable container config | `Env`, `CaptureEnv()`, `ContainerConfig`, `ParseContainer()` |
| `paths/` | Filesystem-root injection | `Roots`, `Default()` |
| `clock/` | Testable time | `Clock`, `System`, `Sleep()`, `Poll()` |
| `process/` | Subprocess execution/supervision/reaping | `Manager`, `Run()`, `StartDaemon()`, `StartProcess()`, `Command`, `Result` |
| `lifecycle/` | Coordinator: shutdown requests, task join, cleanup | `Coordinator`, `ModeRuntime`, `New()`, `Run()`, `RequestShutdown()` |
| `shutdown/` | Stop-approval policy, instructions, drive release | `StopPolicy()`, `StopLoop()`, `Instructions` |
| `weka/` | Weka backend/client container lifecycle | `ensure.go`, `featureflags.go`, `resourcedoc.go`, `release.go`, `ssdproxy.go`, `traces.go` |
| `logrotate/`, `logging/` | log rotation; logger | `logging.New()` |
| `debugexit/` | Pre-exit debug sleep | — |

## Operation packages

`agent`, `syslog`, `persistency`, `ports`, `generation`, `network`, `cos`,
`cpuaffinity`, `drivers`, `wekadrive`, `blockdev`, `adhoc`, `results`,
`resources` — one host operation each (agent, syslog, bind mounts, ports,
generation lock, NIC discovery, hugepages, CPU affinity, driver loading,
drive discovery/signing, block devices, ad-hoc ops, result files,
resources.json).

## Mode → family mapping

| Modes | Family |
|---|---|
| compute, drive, s3, nfs, smbw, data-services | Backend |
| client | Client |
| envoy, telemetry | Auxiliary |
| ssdproxy | SSD proxy |
| drivers-dist | Driver distribution |
| adhoc-op-with-container | Container operation |
| adhoc-op | Task (host ad-hoc) |
| drivers-builder | Driver builder |
| discovery, drivers-loader | Discovery/driver loader (no stop) |

`internal/pkg/domain/runtime_policy.go` holds the operator/runtime-shared
`NeedsOperatorResources()` and `ShutdownInstructions`.

## `runtimes/` layout

One file per family, plus shared helpers: `factory.go` (`New`, `Deps`),
`prepare.go` (`acquirePersistentState`/`startAgent`/`readFeatures`/`generationWatch`),
`container.go` (`buildContainerInput`/`startAndPublish`/`registerCPUAffinity`),
`stem.go` (`startStem`, shared by drivers-dist and adhoc-op-with-container),
`shutdown.go` (`runWekaShutdown`, gated per family by its
`agentLaunched bool`).

Driver install/build: `internal/runtime/drivers` (`Load`, `Build`).

`config/family.go`'s `ContainerConfig`: used by Backend/SSDProxy/DriverDist/
Auxiliary, embedded by `ClientConfig` and `ContainerOpConfig`.

Ad-hoc ops: `adhoc/kernelize.go`, `adhoc/sign_drives.go`, dispatch in `runtimes/adhoc.go`.
NixOS: `drivers/nixos.go` (`PrepareNixosHostKernel`), `internal/pkg/osinfo` (`IsNixos`,
`KernelGccMajor`, `HostNsenterArgs`).
