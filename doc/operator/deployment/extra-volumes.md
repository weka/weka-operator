# Extra Volumes

## Overview

`extraVolumes`/`extraVolumeMounts` let you mount arbitrary Kubernetes
volumes — a ConfigMap, a Secret, a CSI volume, a `hostPath` — into weka pods
and into the operator's own pod, without a custom image. The motivating use
case is mounting a private CA bundle so weka trusts a corporate proxy or a
private Weka Home endpoint; see [weka-home-tls.md](../operations/weka-home-tls.md)
for why a CA bundle is exactly the kind of thing that needs this.

There are three independent surfaces. Each is configured and validated
separately — setting one does not affect the others.

| Surface | Fields | Applies to |
|---|---|---|
| WekaCluster | `spec.podConfig.extraVolumes` + `spec.podConfig.extraVolumeMounts` | Every pod of every WekaContainer owned by the cluster |
| WekaClient | `spec.extraVolumes` + `spec.extraVolumeMounts` (flat, no `podConfig`) | Every client pod |
| Operator (Helm) | `manager.extraVolumes` + `manager.extraVolumeMounts` in `values.yaml` | The operator's own Deployment pod |

WekaContainer carries the same two fields (`spec.extraVolumes` /
`spec.extraVolumeMounts`), but a WekaContainer is normally not
hand-authored: WekaCluster and WekaClient propagate their values down onto
the WekaContainers they own. See [Propagation and rollout](#propagation-and-rollout)
below.

## Validation

`extraVolumes` is schemaless (`*runtime.RawExtension` with
`+kubebuilder:pruning:PreserveUnknownFields`) and accepts whatever JSON a
Kubernetes `volumes` entry would; `extraVolumeMounts` is a typed
`[]corev1.VolumeMount` list, validated by the API server like any other typed
field.

Because `extraVolumes` is schemaless, **the Kubernetes API server does not
validate its contents at all** — it accepts any JSON object shape. The
operator's admission webhook (`cluster_extra_volumes` on WekaCluster,
`client_extra_volumes` on WekaClient) is the only thing that parses it
strictly, rejecting unknown fields so a typo like `mountPahht` is caught
immediately instead of being silently dropped. Both validators are `Error`
severity in both admission modes (strict and relaxed) — see
[admission-control.md](../operations/admission-control.md) for what "mode"
means here.

**If you run with `enableAdmissionControl: false`** (Helm default: `true`),
you lose that check. A structurally invalid value — e.g. an object where a
list is expected — is still caught later, when the pod factory tries to
parse it while building a pod: it returns an error rather than silently
building a pod without your volume. But field-level typos inside an
otherwise well-formed volume (like `mountPahht` above) are not re-checked
there, because `encoding/json` ignores unrecognized fields by default; only
admission's `DisallowUnknownFields` decoding catches those.

WekaContainer itself has no admission validator for these fields — only
WekaCluster and WekaClient are registered for `extra_volumes` validation. A
hand-authored WekaContainer's `extraVolumes`/`extraVolumeMounts` are
validated only by the pod factory at reconcile time.

## Reserved names and paths

The operator reserves certain volume names, name suffixes, and mount-path
prefixes for its own use inside weka/client pods. An `extraVolumes` entry or
`extraVolumeMounts` entry that collides is rejected — at admission time if
enabled, and always by the pod factory as a final backstop. The authoritative
lists live in
[`internal/controllers/resources/extra_volumes.go`](../../../internal/controllers/resources/extra_volumes.go).

**Reserved volume names** (`ReservedVolumeNames`) — every volume name the
operator itself assigns anywhere in a weka pod (backend, client, or init
container). Volumes are pod-scoped, so a name used only by an init container
is reserved too, even though extra mounts never land in init containers (see
[Mount scope](#mount-scope-weka-container-only) below):

```
osrelease, dev, run, sys, weka-boot-scripts, hugepages, smbw-shm,
host-shared-netns, weka-container-persistence-dir, weka-container-shared-dir,
weka-cluster-persistence-dir, weka-container-global-persistence-dir,
weka-proxy-socket-dir, weka-ssdproxy-local-socket, node-info, weka-credentials,
proc-sysrq-trigger, proc-cmdline, devenv, google-cloud-key, host-modules,
host-usr-src, shared-weka-version, otel-packages, libmodules, usrsrc,
gcloud-credentials, wekahome-cacert-secret
```

`wekahome-cacert-secret` is the one name in that list the operator derives at
runtime rather than hardcodes — it comes from `spec.additionalSecrets`, which
forms `<name>-secret`. Only that literal name is reserved, so an ordinary user
name like `corp-ca-secret` is fine.

**Reserved mount-path prefixes** (`ReservedMountPathPrefixes`) — a mount
cannot land on or under:

```
/dev, /sys, /host, /host-binds, /hostside, /opt/weka,
/opt/weka-global-persistence, /var/run/secrets/weka-operator,
/usr/local/bin/weka, /etc/wekaio, /etc/syslog-ng,
/shared-python-packages, /shared-weka-version, /var/log, /lib/modules,
/usr/src, /var/secrets/google
```

**Reserved exact paths** (`ReservedMountPaths`) — operator mounts that are
files rather than directories, so the prefix rule's `/` boundary does not cover
them:

```
/opt/weka_runtime.py, /usr/local/bin/wekaauthcli, /usr/bin/weka, /devenv.sh
```

**`/etc/ssl` and `/etc/pki` are deliberately not reserved.** Mounting a CA
bundle there is exactly the motivating use case for this feature, so those
paths are left free for you to use.

The operator's own Deployment (the Helm `manager.extraVolumes` surface) uses
a much smaller, separate reservation: the volume name `tmpdir` and the mount
path `/tmp`, enforced by a `{{ fail ... }}` in the Helm template itself
rather than by the admission webhook (Helm values aren't admission-checked).

## Mount scope: weka container only

`extraVolumeMounts` mounts land on the **weka container only** — never on any
init container, which runs a different image and may lack the target
directories. Init-container volume names are reserved anyway, because volumes
are pod-scoped.

## Scope

- **One list per object, no per-role targeting.** A WekaCluster's
  `extraVolumes`/`extraVolumeMounts` apply to every pod of every role;
  a WekaClient's apply to every client pod.
- **Mounts may only reference your own volumes.** An `extraVolumeMounts` entry
  must name a volume declared in the same `extraVolumes` list — the operator's
  own volumes cannot be mounted a second time somewhere else.

## Propagation and rollout

WekaCluster's `spec.podConfig.extraVolumes`/`extraVolumeMounts` and
WekaClient's `spec.extraVolumes`/`extraVolumeMounts` are propagated onto the
WekaContainer specs the cluster/client owns, and the pod factory appends them
after every operator-managed volume/mount. A collision is a hard error, not a
silent skip.

### Pods are not recreated automatically

The operator never patches a running pod's volumes, and it does not yet
recreate pods when `extraVolumes`/`extraVolumeMounts` change. A change is
propagated to the owned WekaContainer specs immediately, but the running pods
keep the volumes they were created with. To apply the change, recreate the
affected pods yourself:

```bash
kubectl delete pod <weka-pod> -n <namespace>
```

The replacement pod is built from the updated WekaContainer spec and carries
the new volumes and mounts. Pods created after the change pick it up without
any action. Weka pods ignore SIGTERM and carry a long termination grace
period, so use `--grace-period=0 --force` if a deletion hangs.

## Worked examples

### 1. Mounting a private CA bundle into a cluster's backend pods

The generic mechanism this document describes, applied to the private Weka
Home CA use case from [weka-home-tls.md](../operations/weka-home-tls.md). (Weka
Home itself has a dedicated, simpler field — `wekaHome.cacertSecret`; reach for
`extraVolumes` when the bundle also has to serve some *other* purpose inside the
container. Example 2 combines both.)

```bash
kubectl create secret generic corp-ca-bundle \
  --from-file=ca.crt=./corp-ca.pem
```

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: my-cluster
spec:
  podConfig:
    extraVolumes:
      - name: corp-ca
        secret:
          secretName: corp-ca-bundle
    extraVolumeMounts:
      - name: corp-ca
        mountPath: /etc/ssl/certs/corp-ca.crt
        subPath: ca.crt
        readOnly: true
```

### 2. One Secret, both Weka Home and the rest of the container

`wekaHome.cacertSecret` only makes weka's own Weka Home connection trust the
CA. Nothing else in the container — an outbound HTTPS proxy, `curl`, any other
TLS client — sees it. Point both fields at the same Secret:

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: my-cluster
spec:
  wekaHome:
    cacertSecret: corp-ca-bundle
  podConfig:
    extraVolumes:
      - name: corp-ca-trust
        secret:
          secretName: corp-ca-bundle
    extraVolumeMounts:
      - name: corp-ca-trust
        mountPath: /etc/ssl/certs/corp-ca.crt
        subPath: ca.crt
        readOnly: true
```

The two mounts are independent and both are needed. `wekaHome.cacertSecret`
produces an operator-managed volume named `wekahome-cacert-secret` at
`/var/run/secrets/weka-operator/wekahome-cacert`, staged to
`/opt/weka/k8s-runtime/vars/wh-cacert/cert.pem` — that name and both paths are
reserved, so your `extraVolumes` entry must use a different name and a
non-reserved path. Two volumes referencing one Secret is fine.

Setting `wekaHome.cacertSecret` on a WekaCluster has cluster-wide blast radius —
read [weka-home-tls.md](../operations/weka-home-tls.md#the-ca-path-is-cluster-wide-the-file-is-not)
before doing it.

### 3. A client-only topology (`joinIpPorts`, no `targetCluster`)

Per [weka-home-tls.md](../operations/weka-home-tls.md#client-only-deployments-no-targetcluster),
a WekaClient that joins an external cluster via `spec.joinIpPorts` (instead
of `spec.targetCluster`) has no operator-verified way to know the external
cluster's `weka_cloud_ca_cert_path`, so the supported answer there is
mounting into the container's OS trust store — which `extraVolumes` now
makes declarative instead of requiring a custom image:

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaClient
metadata:
  name: external-client
spec:
  joinIpPorts:
    - "10.0.0.10:14000"
  extraVolumes:
    - name: corp-ca
      secret:
        secretName: corp-ca-bundle
  extraVolumeMounts:
    - name: corp-ca
      mountPath: /etc/ssl/certs/corp-ca.crt
      subPath: ca.crt
      readOnly: true
```

### 4. `manager.extraVolumes` on the operator's own Deployment

For needs of the operator process itself — for example, trusting a
corporate CA for an outbound HTTP(S) proxy. The operator's own Weka Home CR
reporter does **not** need this: it reads its CA Secret through the
Kubernetes API directly into an in-memory certificate pool built on top of
`x509.SystemCertPool()`, so it already has the OS trust store plus whatever
Secret `wekahome.cacertSecret` names, with no volume or mount involved (see
[weka-home-tls.md](../operations/weka-home-tls.md#the-three-configuration-surfaces)).
`manager.extraVolumes` is for anything else the operator container's
filesystem needs.

```yaml
# operator_values.yaml
manager:
  extraVolumes:
    - name: corp-proxy-ca
      configMap:
        name: corp-proxy-ca
  extraVolumeMounts:
    - name: corp-proxy-ca
      mountPath: /etc/ssl/certs/corp-ca.crt
      subPath: ca.crt
```

```bash
helm upgrade --install weka-operator oci://quay.io/weka.io/helm/weka-operator \
  --namespace weka-operator-system --values operator_values.yaml
```
