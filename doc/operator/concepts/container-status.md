# WekaContainer status

## Where the status comes from

`status.status` and `status.internalStatus` of a WekaContainer are read by the container controller from
`weka local ps --json`, executed in the WEKA pod through the Kubernetes API server (`pods/exec`).
`runStatus` becomes `status.status` and `internalStatus.display_status` becomes `status.internalStatus`.
Statuses that proxy a higher-level state (`Completed`, `Deleting`, `Destroying`) are never overwritten by it.

## When the exec channel fails

A failure of the channel itself is different from a command that ran and failed (non-zero exit, unparsable output).
Channel failures are transport errors only: API server 5xx or 429, and connection refused or reset, EOF or network
timeouts of the `pods/exec` connection. Container-side failures reported through the stream (container not running,
runtime exec failure, container or pod not found), the exec's own timeout, TLS and configuration errors and context
cancellation are not channel failures. For a channel failure the operator:

- changes no status and emits no event, in `weka local ps` and in every step that sets the `Error` status;
- skips the drivers check and the drivers reload that a failed `weka local ps` otherwise triggers;
- requeues no sooner than 30s plus up to 30s of random jitter, in all controllers;
- writes `status.status: Unknown`, with an event, once the failure has lasted longer than the CSI `unknownTimeout`
  (default 5m, configuration WekaPolicy `csi.unknownTimeout`). The next successful read replaces it.

The failure streak is kept in operator memory per container, so an operator restart restarts the timeout. A streak
whose last failure is older than 2 minutes starts over.
Command failures keep the previous behaviour.
