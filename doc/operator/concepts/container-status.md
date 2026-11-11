# WekaContainer status

## Where the status comes from

`status.status` and `status.internalStatus` of a WekaContainer are read by the container controller from
`weka local ps --json`, executed in the WEKA pod through the Kubernetes API server (`pods/exec`).
`runStatus` becomes `status.status` and `internalStatus.display_status` becomes `status.internalStatus`.
Statuses that proxy a higher-level state (`Completed`, `Deleting`, `Destroying`) are never overwritten by it.

## When the pod cannot be created

When creating the pod has to wait (for example for feature flags), the status is `PodNotRunning`, except for the
statuses that proxy a higher-level state, and the reason goes into an `EnsurePodWaiting` Warning event, throttled to
one a minute. A failure sets `Error`, with an `EnsurePodError` event. A channel failure, whether or not it is a wait,
is handled as described in [When the API server or exec channel fails](#when-the-api-server-or-exec-channel-fails).

## Stuck ad-hoc and deleting containers

An ad-hoc container (modes `adhoc-op`, `adhoc-op-with-container` and `drivers-loader`) that has no pod and no result
`stuckAdhocPodStartingTimeout` (default 30m) after it was created is deleted, with an `AdhocPodNotCreated` event.
This covers a pod that was never created and one the operator deleted (for example on an unschedulable node). One
whose pod makes no progress for `stuckAdhocPodTimeout` (default 10m), or for `stuckAdhocPodStartingTimeout` while the
pod is still pulling its image or creating containers, is deleted with an `AdhocPodStuck` event. In both cases the requester, while it still needs the result, creates a new one with its
current spec, tolerations included.

A container being deleted for longer than 10 minutes gets a `DeletionSlow` Warning event, at most one every
10 minutes, with the state of its pod.

## When the API server or exec channel fails

A failure of the channel itself is different from a command that ran and failed (non-zero exit, unparsable output).
Channel failures are:

- a `pods/exec` stream that fails before the command reports an exit code (an HTTP 5xx on the connection upgrade, a
  refused or failed dial). An exec that runs past its own timeout is not one: the command hung, so the container-side
  path (drivers check, `Error`) runs;
- any Kubernetes API answer with status 5xx or 429, and network timeouts.

They are not channel failures when the pod's weka container is not running (crash loop, restart, terminated): the
exec then fails with "container not found" or "container not running", which is the container, not the channel.
Command failures (non-zero exit), context cancellation and definitive API answers (not found, forbidden, conflict)
are not channel failures either.

For a channel failure in the steps that set the `Error` status (`EnsurePod`, `ReconcileManagementIPs`,
`ReconcileWekaLocalStatus`), the operator:

- changes no status and emits no event;
- skips the drivers check and the drivers reload that a failed `weka local ps` otherwise triggers;
- writes `status.status: Unknown` with one `ContainerUnreachable` warning event, naming the step and the last error,
  when the operator's last successful `weka local ps` of the container is older than
  `containerHealth.unreachableTimeout` (default 5m, minimum 2m, configuration WekaPolicy). The next successful
  `weka local ps` replaces it.

Unknown is set only for containers whose status comes from `weka local ps` (WEKA process modes); for other modes a
channel failure changes nothing.

In the WekaCluster, WekaClient, WekaContainer, WekaPolicy and WekaManualOperation controllers, a reconcile that ends in a channel failure requeues no sooner than 30s plus up to 30s of random
jitter.

The time of the last successful `weka local ps` is kept in operator memory per container UID and is dropped on the
container's deletion. After an operator restart there is no recorded read, so the first channel failure starts the
clock: a restart only delays Unknown by up to `unreachableTimeout`. A container whose last read is already older than
the timeout becomes Unknown on the first channel failure. A failure on the container side (the weka container is not
running) does not touch the clock. Other failures keep the previous behaviour (`Error` status and a `<Step>Error`
event).
