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

If the configuration WekaPolicy cannot be read within 10 seconds at startup (for example two configuration policies
exist), or its timings are inconsistent, the operator logs `leader-election settings from the configuration WekaPolicy
not usable, using defaults` and starts with the default timings. The ordering is not checked when the policy is
applied, so that log line is the only sign: after a restart, check the operator log or the Lease
(`kubectl -n <namespace> get lease ad0b5146.weka.io -o jsonpath='{.spec.leaseDurationSeconds}'`).
