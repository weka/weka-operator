# Debugging the in-cluster operator with Delve

Runs the operator manager inside its own pod under `dlv exec`, so an IDE can attach to it with breakpoints.

The operator has to run in the cluster for most flows: it calls node-agent pods by pod IP
(`http://<podIP>:8090/...`, e.g. `internal/controllers/wekacontainer/metrics_steps.go`), which a laptop can't reach.
`make debugcontroller` (local `dlv debug`) only works for flows that never talk to node-agent.

## Deploy

Prerequisites: the operator is already installed with Helm, `docker buildx` is logged in to the registry, and
`KUBECONFIG` points at the target cluster.

```bash
make deploy-debug
# or, with overrides:
REPO=quay.io/weka.io/weka-operator-dev GOARCH=amd64 NAMESPACE=weka-operator-system ./script/deploy-debug.sh
```

The script:
1. builds the manager with `-gcflags="all=-N -l"` into `bin/weka-operator-debug`;
2. builds `debug.Dockerfile` (the binary plus a pinned `dlv`) and pushes it with a unique tag;
3. patches the manager container of the existing Deployment in one change: debug image, `dlv exec … --headless --listen=127.0.0.1:2345 --continue` (reachable only through port-forward), no liveness/readiness probes, `ENABLE_LEADER_ELECTION=false`.

Helm values are untouched. CRDs are not applied: if the branch changes CRD types, apply them first:

```bash
make crd && kubectl apply --server-side -f charts/weka-operator/crds/
```

## Attach

```bash
kubectl -n weka-operator-system port-forward deploy/weka-operator-controller-manager 2345:2345
```

- GoLand: Run → Edit Configurations → Go Remote, host `localhost`, port `2345`.
- VS Code: a `"request": "attach", "mode": "remote"` Go launch config on port 2345.

The binary is built from the local checkout, so source paths match without path mapping.

## Things to know

- `--continue` starts the operator without waiting for a debugger; attaching doesn't restart it.
- A hit breakpoint pauses the whole process: every reconcile, for every object, waits until you resume.
- Leader election is disabled because a paused process can't renew its lease, and the manager exits with
  `leader election lost`. Keep the Deployment at one replica.
- The port-forward can take a few seconds to print `Forwarding from` on slow API servers.
- Changing an env var on the Deployment (`kubectl set env`) restarts the pod and keeps the dlv command; re-attach after.

## Restore

```bash
./script/deploy-debug.sh restore     # kubectl rollout undo: back to the revision before the debug patch
```

Or run your usual `helm upgrade` for the operator, which re-renders the manager container.
