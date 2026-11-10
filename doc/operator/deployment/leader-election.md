# Leader-election timings

The operator manager's leader-election lease timings live in the configuration WekaPolicy (the one policy of this
kind in the operator namespace), under `spec.payload.configurationPayload.leaderElection`. They matter only when
leader election is enabled (Helm `enableLeaderElection`); with it disabled the policy is not read for them. Every field is optional and an unset field keeps its default.

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaPolicy
metadata:
  name: weka-configuration
  namespace: weka-operator-system
spec:
  payload:
    configurationPayload:
      leaderElection:
        leaseDuration: 60s
        renewDeadline: 40s
        retryPeriod: 5s
```

| Field | Default |
|---|---|
| `leaseDuration` | `15s`; how long other replicas wait before taking over the lease |
| `renewDeadline` | `10s`; how long the leader retries renewing the lease before it gives up leadership and exits |
| `retryPeriod` | `2s`; the interval between lease acquire and renew attempts |

The values must satisfy `leaseDuration > renewDeadline > 1.2 × retryPeriod > 0`, with `leaseDuration` rounded down to
whole seconds (the Lease object stores whole seconds, and other replicas judge expiry from it): `leaseDuration: 10.5s`
with `renewDeadline: 10.2s` is inconsistent. `leaseDuration` can be at most 2147483647 seconds (the Lease field is a
32-bit integer). A partial override counts with the defaults of the unset fields: setting only
`renewDeadline: 30s` leaves `leaseDuration` at `15s`, which is inconsistent.

## When to raise them

If the leader cannot renew the lease within `renewDeadline`, for example because API requests are slow, it gives up
leadership and the manager pod restarts after logging `problem running manager` with `leader election lost`; every
restart re-queues every WekaContainer. Raising the timings, for example to the values above, gives renewals more time.

## Applying a change

The timings are read once, when the operator pod starts. After changing them, restart the operator:

```
kubectl -n <namespace> rollout restart deployment <prefix>-controller-manager
```

At startup the operator reads the configuration WekaPolicy, retrying channel failures for up to 30 seconds with a
2-second pause between attempts (each request is limited to 10 seconds). The timings used:

| Outcome of the read | Timings |
|---|---|
| Read OK and consistent, or no configuration policy | the policy's, or the defaults |
| Read OK but inconsistent | the defaults, after logging `leader-election settings from the configuration WekaPolicy not usable, using defaults` |
| Fails on the channel to the API server (timeout, `429`, `5xx`, network timeout) and still fails after 30 seconds | `leaseDuration: 60s`, `renewDeadline: 40s`, `retryPeriod: 5s`, after logging `using slow-API leader-election settings` |
| Fails any other way (for example missing permissions, more than one configuration policy) | the defaults at once, after logging `could not be read, using default leader-election settings` |

The ordering is not checked when the policy is applied, so the log line is the only sign: after a restart, check the
operator log or the Lease
(`kubectl -n <namespace> get lease ad0b5146.weka.io -o jsonpath='{.spec.leaseDurationSeconds}'`).
