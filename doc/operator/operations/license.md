# WEKA License from a Secret

Set `spec.licenseSecret` on a WekaCluster to have the operator apply the WEKA
license, instead of running `weka cluster license set` by hand.

## Secret format

The Secret lives in the WekaCluster's namespace and holds the license under
the key `license`. It can be created by hand or by a secret manager such as
External Secrets Operator or Vault.

```bash
kubectl create secret generic weka-license -n <namespace> --from-literal=license=<license>
```

```yaml
spec:
  licenseSecret: weka-license
```

## Behaviour

- The license is applied after the cluster forms, from an active backend
  container, with `weka cluster license set`. The license is never written to
  operator logs, traces, events or status.
- The step runs at most once a minute. It re-applies the license whenever the
  Secret's UID or resourceVersion changes, so a renewed license is picked up
  without touching the WekaCluster. Any update to the Secret, including a
  metadata-only one, causes a (harmless) re-apply. There is no Secret watch,
  so a change can take up to a minute to apply.
- A failure does not block the cluster from becoming Ready; the step is
  retried a minute later.
- Clearing `spec.licenseSecret` does not remove the license from the cluster.

## Status

| Signal | Meaning |
|---|---|
| Condition `LicenseApplied=True` | License applied. The message is `<secret UID>/<resourceVersion>` of the applied Secret. |
| Condition `LicenseApplied=False`, reason `ApplyFailed` | Last attempt failed; the message says why (missing Secret or key, no active container, CLI failure). |
| Warning event `LicenseApplyFailed` on the WekaCluster | Emitted on every failed attempt. |

CLI error output is not included in the event or condition, because it may
echo the license. Check the license with `weka cluster license` from a
backend container.
