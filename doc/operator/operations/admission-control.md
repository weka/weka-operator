# Admission Control

## Overview

The operator runs a validating admission webhook for `WekaCluster` and `WekaClient`
resources. On every `kubectl apply` (or Helm/GitOps equivalent) it runs a battery
of policies and either admits the request, attaches a `kubectl Warning:` line, or
rejects it. The default posture is non-blocking — most policies emit warnings;
only feasibility-breaking specs are rejected.

## Configuration

Three Helm knobs in `charts/weka-operator/values.yaml`:

```yaml
# 1. Master switch
enableAdmissionControl: true

# 2. Default posture for all policies
admissionPolicies:
  mode: relaxed       # or "strict"

  # 3. Per-policy overrides (commented out by default)
  # policies:
  #   cluster_signed_drives:    error
  #   cluster_min_drives_large_floor: warn
```

| Knob | Type | Default | Effect |
|---|---|---|---|
| `enableAdmissionControl` | bool | `true` | When `false`, the entire pipeline is removed: no webhook server, no `ValidatingWebhookConfiguration`, no validation. Use only for emergency recovery or migration windows. |
| `admissionPolicies.mode` | `strict` \| `relaxed` | `relaxed` | Picks the strict or relaxed column from each policy's defaults pair. |
| `admissionPolicies.policies.<id>` | `default` \| `warn` \| `error` | `default` | Pin one specific policy regardless of the global mode. |

The full list of policies, what each one checks, and the `(strict, relaxed)`
defaults for each are documented inline in `charts/weka-operator/values.yaml`
under `admissionPolicies`.

## Example output

### Warning (advisory; resource still applied)

A spec that violates several advisory policies — the resource is created, the
operator surfaces every violation as a `Warning:` line so you can fix them all
in one pass.

```
$ kubectl apply -f my-cluster.yaml
Warning: spec.dynamicTemplate.driveContainers: Invalid value: 12: spec.dynamicTemplate.driveContainers (12) exceeds the number of nodes matching the "drive"-role selector (6). The cluster cannot deploy 12 drive containers on 6 node(s); some containers will fail to schedule. Reduce driveContainers or label more nodes.
Warning: spec.dynamicTemplate.numDrives: Invalid value: 5: spec.dynamicTemplate.driveContainers × numDrives (12 × 5 = 60) exceeds the total signed and non-blocked drives across 6 matched drive node(s) (6). Some drive containers will not be able to claim a drive. Reduce numDrives, reduce driveContainers, sign more drives, or label more nodes.
wekacluster.weka.weka.io/cluster-dev created
```

### Rejection

A spec that violates a feasibility policy (`cluster_min_drives_feasibility` —
the IO-start condition can never be satisfied). Advisory warnings are still
listed; the rejection comes last and the resource is **not** created.

```
$ kubectl apply -f my-cluster.yaml
Warning: spec.startIoConditions.minNumDrives: Invalid value: 100: spec.startIoConditions.minNumDrives (100) is set on a small cluster (driveContainers=6, numDrives=1). For clusters of this size, minNumDrives is usually unnecessary and may cause unexpected behavior. Consider omitting the field.
Error from server (Forbidden): error when creating "my-cluster.yaml": admission webhook "validate.wekacluster.weka.io" denied the request: spec.startIoConditions.minNumDrives: Invalid value: 100: spec.startIoConditions.minNumDrives (100) exceeds total drive capacity (6 × 1 = 6). The cluster will never satisfy the IO-start condition. Reduce minNumDrives or increase driveContainers / numDrives.
```

## Posture: strict vs relaxed

- **Relaxed** (default): most policies emit warnings so non-compliant specs
  remain editable; only "this will never work" specs are rejected.
- **Strict**: tightens correctness checks to outright rejection.

Per-policy overrides win over the global mode. To stay relaxed globally but
enforce one specific check, set
`admissionPolicies.policies.<id>: error` in your values.

## Escape hatches

| Layer | Mechanism | Use case |
|---|---|---|
| Per-object | `weka.io/skip-admission` label on the CR (any value) | Recovery from a stuck spec, deliberate one-off bypass. Matches inside the API server, so it works even when the operator pod is unreachable. |
| Per-policy | `admissionPolicies.policies.<id>: warn \| error` | Demote noisy policies or promote critical ones without changing global mode. |
| Cluster-wide | `enableAdmissionControl: false` | Outage, migration, or kill-switch. Effective on the next operator restart. |
| Emergency | `kubectl delete validatingwebhookconfiguration weka-operator-validating-webhook-configuration` | Operator pod unreachable and the master switch can't be flipped. The next operator pod recreates the configuration. The name is `<prefix>-validating-webhook-configuration` — adjust if you customized the chart `prefix`. |

## Operator restart behavior

The webhook is configured with `failurePolicy: Fail`, meaning the API server
rejects writes when it can't reach the webhook service. To prevent this from
extending across rolling restarts, the operator pod is gated by a dedicated
readiness check that waits for the webhook server to bind `:9443` before
flipping Ready.
