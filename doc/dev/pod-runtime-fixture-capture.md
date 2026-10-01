# Capturing Real-System Fixtures for the Pod Runtime

## Purpose

The Go pod runtime (`internal/runtime/`) is tested against synthetic
fixtures in `internal/runtime/testdata/` (see that directory's `README.md`).
Synthetic fixtures are sufficient for asserting intended behavior and stay
in place. This guide is for an agent later handed a `KUBECONFIG` who extends
that coverage with captures from a real, running cluster.

Everything here is read-only collection of already-running system state. It
never starts, stops, installs, signs, mounts, or provisions anything, and
never manufactures a failure by mutating a real system.

## Step 0: Confirm before collecting anything

Before running any command, state explicitly and get it confirmed:

1. **Context** — which kube-context / cluster the supplied `KUBECONFIG` points at.
2. **Namespace** — which namespace holds the target pods.
3. **Pods** — the exact pod names (or the `kubectl get pods -n <ns>` you'll run first to pick them).
4. **Scope** — which candidate captures below you intend to take, and the mode/scenario they represent (e.g. "backend container, steady state", "client container, mid-shutdown").

Do not widen scope mid-capture without re-confirming. Use the kubeconfig
only for local `kubectl` invocations — never copy the file, its contents,
or any token from it into a fixture, a log excerpt, or this repo.

## Read-only command allowlist

Only the commands below are permitted. Anything not listed is out of scope
even if it looks harmless — ask instead of improvising.

| Command | Use |
|---|---|
| `kubectl get pods -n <ns> [-o wide\|json]` | Enumerate candidate pods |
| `kubectl describe pod <pod> -n <ns>` | Pod spec/status/events |
| `kubectl logs <pod> -n <ns> [-c <container>] [--previous]` | Log capture |
| `kubectl exec <pod> -n <ns> [-c <container>] -- cat <path>` | Read one file |
| `kubectl exec <pod> -n <ns> [-c <container>] -- ls <path>` | List a directory |
| `kubectl exec ... -- weka local ps --json` | Container list |
| `kubectl exec ... -- weka local resources -C <name> --json` | Per-container resources |
| `kubectl exec ... -- weka version -J` | Installed/active version list (read-only; never `weka version set/get`) |
| `kubectl exec ... -- ip -o addr` | Interface/address listing |
| `kubectl exec ... -- cat /proc/wekafs/interface` | Mounted client interface info |

`cat` and `ls` are the only shell-outs permitted inside `exec`; never chain
them with `&&`/`;` into something that writes, and never pipe into another
binary. If a candidate file needs a command not on this list, stop and ask
rather than substituting something adjacent.

### Explicitly prohibited, even framed as "just checking"

- `weka local start/stop/attach/remove`, mutating `weka cluster ...` verbs,
  `weka version set/get`, driver signing (`weka local resources drive ...`
  writes), `mount`/`umount`, `dd`, cloud-CLI provisioning
  (`aws`/`gcloud`/`az` create/delete), or any bundled script that performs
  those actions.
- Restarting, deleting, cordoning, or draining anything to "see what a
  failure fixture looks like." A failure-path fixture only comes from a
  system already in that state; go find one, don't cause one.
- Any diagnostic script not itself composed only of allowlisted commands,
  however official-looking (`weka debug`, support bundles, `weka_runtime.py`
  reruns).

## Capture metadata

Every capture (a directory of files, or a single JSON/text blob) must ship
with a sibling `meta.json` describing it:

```json
{"mode": "backend", "scenario": "steady-state-compute", "weka_version": "4.4.10",
 "cli_variant": "weka local ps --json", "os": "Ubuntu 22.04", "kernel": "5.15.0-1053-aws",
 "arch": "amd64", "runtime_implementation": "go", "captured_at": "2026-09-21T00:00:00Z",
 "synthetic": false, "notes": ""}
```

- `mode` matches a runtime mode/family name from `internal/runtime/runtimes`
  or `internal/runtime/config/family.go` (e.g. `backend`, `client`,
  `drivers-dist`, `ssdproxy`, `adhoc`).
- `runtime_implementation` is `go` or `python` — whichever produced the
  artifact (`weka_runtime.py` may still run on older nodes; note it if so).
- `synthetic` is always `false` here, so a reviewer scanning `meta.json`
  files can't confuse a real capture with a hand-written `testdata/` one.
- `notes` covers anything else, including "unavailable" cases (see below).

## Sanitization

Apply these substitutions consistently across every file in a capture, so
the same real value always maps to the same placeholder within a capture
(and ideally across captures, so cross-references still line up).

| Data | Placeholder pattern | Notes |
|---|---|---|
| Credentials, tokens, API keys | `REDACTED-TOKEN-<n>` | Replace the whole value, never partially mask |
| Cluster/join endpoints, URLs | `endpoint-<n>.example` | Keep scheme/port shape if the test cares |
| Hostnames, node names | `node-<n>` | Same node → same placeholder everywhere in the capture |
| IP addresses | `10.0.0.<n>` / `fd00::<n>` | Keep subnet/prefix shape if the fixture tests subnet matching |
| MAC addresses | `02:00:00:00:00:<nn>` | Locally-administered range, obviously fake |
| UUIDs (cluster GUID, drive UUIDs, pod/boot IDs) | `11111111-...`, `22222222-...`, sequential | A virtual drive's `physicalUUID` must still match its physical drive's UUID elsewhere in the capture |
| Serial numbers | `SN-<nnnn>` | |
| Container/pod names leaking customer info | `container-<n>` / `pod-<n>` | A generic name like `compute-0` needs no change |

Run a final grep for the customer/cluster name and any raw IP octets you
still recognize before committing — automated substitution misses strings
embedded in free-text log lines.

## What must survive sanitization

Sanitize values, never structure. A test asserting on shape breaks silently
if sanitization also erases the shape. Preserve:

- **Presence vs. absence vs. null vs. empty.** A field absent in the
  source (like `release_no_bitmap.spec`'s `"feature_flags": null`) stays
  absent/null, not `""` or `0`.
- **Numeric precision.** Generations and similar counters can exceed 2^53
  (see `weka-resources.json`'s `generation`); keep the real digit count
  even when replacing the value, so float-precision bugs still get caught.
- **Identifier relationships.** If a virtual drive's UUID, a physical
  drive's UUID, and a `weka local resources` listing refer to the same
  drive, they must still refer to the same drive after sanitization, and
  distinct real drives must stay distinct.
- **Unknown/forward-compatible fields.** Leave unrecognized JSON keys in
  place (renamed like everything else) instead of stripping them — these
  fixtures also test that the runtime round-trips fields it doesn't know.

## Marking synthetic vs. real, and documenting the unavailable

- Every file from this process gets `"synthetic": false` in its
  `meta.json`, even after full sanitization — sanitized-but-real is a
  distinct category from hand-written, since it can still carry real-world
  quirks (field ordering, whitespace, an exact error string) a hand-written
  fixture would not.
- If a candidate capture is unreachable on the confirmed cluster (no pod
  currently mid-shutdown, or `/proc/wekafs/interface` absent because no
  client is mounted), do not fabricate it. Record the gap — a `NOTES.md`
  alongside the attempted captures, or a line in the PR/task description —
  naming the scenario and why it wasn't available.

## Where captures go

```
internal/runtime/testdata/captures/<mode>/<scenario>/
  meta.json
  <captured files, named after their source path's basename
   or the command that produced them>
```

Example: `internal/runtime/testdata/captures/backend/steady-state-compute/`
holding `resources.json`, `weka-local-ps.json`, `meta.json`.

Keep captures separate from the existing synthetic files — tests that want
a real-world sample opt in explicitly by pointing at `captures/...`; the
synthetic fixtures stay the default coverage per `testdata/README.md`.

## Candidate captures

Pick from this list per the confirmed scope; it's not a checklist to
exhaust in one pass.

| Capture | Source | Mirrors testdata shape |
|---|---|---|
| Container resource allocation | `cat /opt/weka/k8s-runtime/resources.json` | `resources.json` |
| Container list | `weka local ps --json` | — |
| Per-container resources | `weka local resources -C <name> --json` | — |
| Weka version list | `weka version -J` | — |
| Release/feature-flag spec | `cat /opt/weka/dist/release/<file>.spec` | `release_*.spec` |
| Weka-owned resource document | `cat /opt/weka/data/<name>/container/resources.json` | `weka-resources.json` |
| Shutdown instructions | `cat /host-binds/shared/instructions/<pod>/<boot>/shutdown_instructions.json` | `shutdown_instructions_*.json` |
| Runtime result | `cat /weka-runtime/results.json` | `driver_loader_results.json`, `discovery_results.json` |
| Network interfaces | `ip -o addr` | — |
| Client mount interface | `cat /proc/wekafs/interface` | — |
| Pod description/events, logs | `kubectl describe pod`, `kubectl logs` | — |

## Using captures once collected

Real captures extend coverage; they never replace the behavioral
assertions the synthetic fixtures exist to make. A test may load a real
capture to prove a specific shape parses, but intended-behavior tests keep
running against the synthetic, hand-controlled fixtures.
