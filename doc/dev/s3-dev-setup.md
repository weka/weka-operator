# S3 Dev Setup Guide

This guide covers how to configure Weka S3, deploy a local load balancer (Sidekick), and run a sustained S3 workload (Goader) for testing in a dev operator environment.

## Overview

The setup has three layers:

1. **Weka S3 cluster** — S3 service running inside Weka pods, fronted by Envoy on port 35240 (TLS, self-signed cert)
2. **Sidekick LB** — Kubernetes DaemonSet running on each k3s node, listening on `hostPort: 50002`, load-balancing across both Envoy endpoints
3. **Goader workload** — Kubernetes DaemonSet on each k3s node writing/reading 1 MiB objects to exercise the S3 path

Goader talks to Sidekick via `http://localhost:50002`, so the TLS termination and backend selection is handled transparently by Sidekick.

### Working example values

| Parameter | Value |
|-----------|-------|
| WekaCluster | `default/anton-test` |
| Weka image | `quay.io/weka.io/weka-in-container-dev:5.1.0.8-8.20` |
| S3 envoy endpoints (host IPs) | `https://172.31.5.84:35240`, `https://172.31.6.240:35240` |
| S3 envoy endpoints (weka-side IPs) | `10.200.5.84:35240`, `10.200.6.240:35240` |
| Sidekick port | `50002` |
| Bucket | `goader-data` |
| S3 user / secret | `goader-user` / `goaderSecret123` |
| Filesystem | `default` (25 TiB) |

---

## 1. WekaCluster Prerequisites

The WekaCluster spec must have enough S3 and data-services containers. The working configuration uses:

```yaml
spec:
  dynamicTemplate:
    s3Containers: 2
    dataServicesContainers: 2
    dataServicesExtraCores: 8   # gives 11 cores per data-services pod
```

Wait until all pods are Running before proceeding:

```bash
kubectl get pods -n default -w
```

---

## 2. Weka CLI Setup

All Weka CLI commands run from inside a data-services pod.

```bash
# One-liner to get any data-services pod (not a frontend)
DS_POD=$(kubectl get pods -n default | grep 'data-services' | grep -v fe | head -1 | awk '{print $1}')
```

### Verify S3 cluster is active

```bash
kubectl exec -n default $DS_POD -- weka s3 cluster status
```

Expect `active: true`, `port: 35240`, `filesystem: default`.

### Resize filesystem (if needed)

The default filesystem may need to be large enough for the test data. For 2,880,000 × 1 MiB objects per node:

```bash
kubectl exec -n default $DS_POD -- weka fs update default --total-capacity 25TiB
```

### Create bucket, user, and policy

```bash
# Create bucket
kubectl exec -n default $DS_POD -- weka s3 bucket create goader-data

# Create S3 user (type "s3", not "regular")
kubectl exec -n default $DS_POD -- weka user add goader-user s3 goaderSecret123

# Attach readwrite policy to the user
kubectl exec -n default $DS_POD -- weka s3 policy attach readwrite goader-user
```

### Set catalog scan interval (optional)

Weka auto-caps S3 lifecycle retention at 7 days. The scan interval controls how often lifecycle tasks run:

```bash
kubectl exec -n default $DS_POD -- weka dataservice s3-lifecycle-task set-interval --interval 30m
```

---

## 3. Sidekick Load Balancer

Sidekick is a tiny reverse proxy from MinIO. It runs as a DaemonSet with `hostNetwork: true` so that Goader (also on the host network) can reach it at `localhost:50002`.

### Key details

- Uses `--insecure` to skip TLS verification on the self-signed Envoy certs
- Health-check path: `/wekas3api/health/ready`
- **Port matching**: `--address` port, `containerPort`, and `hostPort` must all be the same number
- **TIME-WAIT gotcha**: After deleting and recreating the DaemonSet on the same port, kernel TIME-WAIT sockets hold the port for ~2 minutes. Check with `ss -tnp | grep 50002`; if sockets are present, either wait or use a different port number.

### Sidekick DaemonSet YAML

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: sidekick-lb
  namespace: default
spec:
  selector:
    matchLabels:
      app: sidekick-lb
  template:
    metadata:
      labels:
        app: sidekick-lb
    spec:
      hostNetwork: true
      containers:
        - name: sidekick
          image: quay.io/minio/sidekick:v7.1.1
          args:
            - "--address=:50002"
            - "--insecure"
            - "--quiet"
            - "--host-balance=random"
            - "--health-path=/wekas3api/health/ready"
            - "https://172.31.5.84:35240,https://172.31.6.240:35240"
          ports:
            - containerPort: 50002
              hostPort: 50002
              name: lb
```

> **Finding Envoy host IPs**: `kubectl get pods -n default -o wide | grep envoy` — use the NODE column to look up each node's IP, or inspect pod annotations.

### Deploy and verify

```bash
kubectl apply -f sidekick-lb.yaml

# Confirm pods are Running on every node
kubectl get pods -n default -l app=sidekick-lb -o wide

# Test health endpoint from any k3s node (or a pod on host network)
curl -sk https://172.31.5.84:35240/wekas3api/health/ready
curl http://localhost:50002/wekas3api/health/ready
```

Both should return HTTP 200.

---

## 4. Goader Workload

Goader is a Weka-built S3 load generator. It runs as a DaemonSet alongside Sidekick, pointing at `http://localhost:50002`.

### Object key pattern

Goader uses `--url` to specify the object key template. The `${NODE_NAME}` downward API env var namespaces writes per node so all nodes write to distinct key prefixes. `NNN/NNNN` in the pattern generates a two-level numeric directory hierarchy (`000`–`999` / `0000`–`9999`).

### Write DaemonSet

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: goader-s3-write
  namespace: default
  labels:
    app: goader-s3-write
spec:
  updateStrategy:
    type: RollingUpdate
    rollingUpdate: { maxUnavailable: 100% }
  selector:
    matchLabels:
      app: goader-s3-write
  template:
    metadata:
      labels:
        app: goader-s3-write
    spec:
      hostNetwork: true
      terminationGracePeriodSeconds: 30
      nodeSelector:
        node.kubernetes.io/instance-type: k3s
      containers:
        - name: goader
          image: public.ecr.aws/weka/goader:v1.4.15
          imagePullPolicy: IfNotPresent
          resources:
            requests: { cpu: "4" }
          env:
            - name: NODE_NAME
              valueFrom:
                fieldRef:
                  fieldPath: spec.nodeName
          command: ["/bin/sh", "-c"]
          args:
            - |
              exec goader \
                -wt 8 \
                --requests-engine s3 \
                --s3-endpoint "http://localhost:50002" \
                --s3-bucket goader-data \
                --s3-region local \
                --s3-api-key goader-user \
                --s3-secret-key goaderSecret123 \
                --body-size 1MiB \
                --max-requests 2880000 \
                --url "goader/${NODE_NAME}/NNN/NNNN" \
                --show-progress=false --verbose
```

With 8 worker nodes and 2,880,000 objects/node × 1 MiB = ~22.5 TiB total, which fills ~90% of a 25 TiB filesystem.

### Read DaemonSet

Use more read threads (`-rt`) since reads are typically CPU-bound on the client side:

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: goader-s3-read
  namespace: default
  labels:
    app: goader-s3-read
spec:
  updateStrategy:
    type: RollingUpdate
    rollingUpdate: { maxUnavailable: 100% }
  selector:
    matchLabels:
      app: goader-s3-read
  template:
    metadata:
      labels:
        app: goader-s3-read
    spec:
      hostNetwork: true
      terminationGracePeriodSeconds: 30
      nodeSelector:
        node.kubernetes.io/instance-type: k3s
      containers:
        - name: goader
          image: public.ecr.aws/weka/goader:v1.4.15
          imagePullPolicy: IfNotPresent
          resources:
            requests: { cpu: "6" }
          env:
            - name: NODE_NAME
              valueFrom:
                fieldRef:
                  fieldPath: spec.nodeName
          command: ["/bin/sh", "-c"]
          args:
            - |
              exec goader \
                -rt 32 \
                --requests-engine s3 \
                --s3-endpoint "http://localhost:50002" \
                --s3-bucket goader-data \
                --s3-region local \
                --s3-api-key goader-user \
                --s3-secret-key goaderSecret123 \
                --body-size 1MiB \
                --max-requests 10000000 \
                --url "goader/${NODE_NAME}/NNN/NNNN" \
                --show-progress=false --verbose
```

### Monitor progress

```bash
# Tail logs from one goader pod
kubectl logs -n default -l app=goader-write -f --tail=20

# Object count in bucket (from data-services pod)
kubectl exec -n default $DS_POD -- weka s3 bucket stat goader-data
```

---

## 5. Verification

```bash
# S3 cluster up
kubectl exec -n default $DS_POD -- weka s3 cluster status

# Sidekick healthy
curl http://localhost:50002/wekas3api/health/ready   # from any k3s node

# Goader writing (expect non-zero request count in logs)
kubectl logs -n default -l app=goader-write --tail=5

# Filesystem usage growing
kubectl exec -n default $DS_POD -- weka fs
```

---

## 6. Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| Sidekick: `All backends are down` | Health check failing | `curl -sk https://<envoy-ip>:35240/wekas3api/health/ready` — must return 200; check Envoy pod health |
| Goader: `502 Bad Gateway` | Sidekick can't reach Envoy backends | Same as above; also confirm Sidekick pods are Running |
| Goader: `connection refused on :50001` | Pod created before port config was applied | Delete and recreate the DaemonSet |
| Sidekick won't bind port after redeploy | TIME-WAIT sockets | `ss -tnp | grep 50002`; wait ~2 min or use a different port |
| S3 returns 403 | User missing policy | `weka s3 policy attach readwrite goader-user` |
| S3 cluster not active | Not enough licensed nodes / pods not ready | `weka cluster status`; wait for all pods Running |

### Quick diagnostic sequence

```bash
# 1. Are all expected pods running?
kubectl get pods -n default -o wide

# 2. Is S3 healthy?
kubectl exec -n default $DS_POD -- weka s3 cluster status

# 3. Can we reach each Envoy endpoint directly?
curl -sk https://172.31.5.84:35240/wekas3api/health/ready
curl -sk https://172.31.6.240:35240/wekas3api/health/ready

# 4. Is Sidekick proxying correctly?
curl http://localhost:50002/wekas3api/health/ready

# 5. Are there TIME-WAIT sockets on the Sidekick port?
ss -tnp | grep 50002
```
