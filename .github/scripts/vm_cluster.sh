#!/usr/bin/env bash
# Used by drive_sharing.yaml. A k3s cluster on Scalar backend VMs, through Port:
#   acquire NAME N TTL - Port Machine Request for N VMs under claim NAME with a k3s master; waits until
#                        it is Done (a `rejected` request means the VM pool is busy: retried every 2 min
#                        for up to 60 min), then until Port links the cluster's kubeconfig.
#                        Writes step output vault_path (KV v2 mount `secrets`, key `kubeconfig`).
#   release NAME       - releases every machine in claim NAME; never fails. The request TTL is the backstop.
# Env: PORT_API_URL, PORT_CLIENT_ID, PORT_CLIENT_SECRET; optional PORT_RUN_AS: email of the Port user the
# Machine Request runs as (it becomes the claim owner); ignored when Port has no such user.
set -euo pipefail

log() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] $*"; }

port() {  # fresh token per call: the waits outlive it
  local t retry="--retry 3 --retry-delay 5" limit=30
  # A retried action POST can file a second Machine Request, so it gets one longer attempt instead.
  [[ $1 == -X ]] && retry="" limit=60
  # --max-time: release runs in GitHub's short cancel cleanup window, so a stalled Port must not hang it.
  t=$(curl -sSf --max-time 30 --retry 2 --retry-delay 5 -X POST "$PORT_API_URL/auth/access_token" -H 'Content-Type: application/json' \
    -d "$(jq -n --arg id "$PORT_CLIENT_ID" --arg s "$PORT_CLIENT_SECRET" '{clientId: $id, clientSecret: $s}')" \
    | jq -r .accessToken)
  curl -sSf --max-time "$limit" $retry "$@" -H "Authorization: Bearer $t" -H 'Content-Type: application/json'
}

queue() {  # the claim's request-queue entries, oldest first; a search is read-only, so it keeps the retries
  port "$PORT_API_URL/entities/search" -d "$(jq -n --arg c "$name" '{combinator: "and", rules: [
      {property: "$blueprint", operator: "=", value: "lab_server_claim_request_queue"},
      {property: "name", operator: "=", value: $c}]}')" \
    | jq -c '.entities|sort_by(.createdAt)'
}

# Sets result to Done, rejected, failed or timeout (15 min without a final status).
request_once() {
  local old s
  old=$(queue | jq -c 'map(.identifier)') || { echo "::error::cannot read Port request queue"; exit 1; }
  port -X POST "$PORT_API_URL/actions/request/runs$run_as" -d "$(jq -n --arg c "$name" --argjson n "$n" --arg ttl "$ttl" \
    '{properties: {name: $c, vm_backends_number: $n, backends_number: 0, clients_number: 0,
                   k8s_cluster: $c, master: true, ttl: $ttl}}')" | jq -c '{ok, run: .run.id}' \
    || { echo "::error::Port Machine Request POST for claim $name failed"; exit 1; }
  for _ in $(seq 45); do
    sleep 20
    # newest request that did not exist before our POST; "" until it shows up
    s=$(queue | jq -r --argjson old "$old" 'map(select(.identifier|IN($old[])|not))|last|.properties.status // ""') || s=""
    log "request: ${s:-pending}"
    case $s in
      Done|done) result=Done; return ;;
      rejected|failed) result=$s; return ;;
    esac
  done
  result=timeout
}

acquire() {
  run_as=""  # global: request_once appends it to the POST
  if [[ -n ${PORT_RUN_AS:-} ]]; then
    if port "$PORT_API_URL/users/$PORT_RUN_AS" >/dev/null 2>&1; then
      run_as="?run_as=$(jq -rn --arg e "$PORT_RUN_AS" '$e|@uri')"
      log "Machine Request runs as $PORT_RUN_AS"
    else
      echo "::warning::$PORT_RUN_AS is not a Port user: the claim is owned by the CI service user"
    fi
  fi
  local deadline=$((SECONDS + 3600)) result ref vp=""
  while :; do
    log "requesting $n VMs for claim $name (ttl $ttl)"
    request_once
    case $result in
      Done) break ;;
      rejected)
        if (( SECONDS + 120 >= deadline )); then
          echo "::error::VM pool busy: request for $n VMs still rejected after 60m"
          exit 1
        fi
        log "rejected (VM pool busy), retrying in 120s"
        sleep 120 ;;
      *) echo "::error::Port Machine Request for claim $name: $result"; exit 1 ;;
    esac
  done
  # kubeporter links k8s_cluster/NAME -(kubeconfig)-> vault_reference {vault_path} once bliss stored it.
  for _ in $(seq 45); do  # 15 min
    ref=$(port "$PORT_API_URL/blueprints/k8s_cluster/entities/$name" | jq -r '.entity.relations.kubeconfig // empty') || ref=""
    if [[ -n $ref ]]; then
      vp=$(port "$PORT_API_URL/blueprints/vault_reference/entities/$ref" | jq -r '.entity.properties.vault_path // empty') || vp=""
      [[ -n $vp ]] && break
    fi
    log "waiting for the kubeconfig reference of k8s_cluster/$name"
    sleep 20
  done
  if [[ -z $vp ]]; then
    echo "::error::no kubeconfig reference for k8s_cluster/$name in Port after 15m"
    exit 1
  fi
  log "kubeconfig at vault path $vp"
  echo "vault_path=$vp" >> "$GITHUB_OUTPUT"
}

release() {
  port -X POST "$PORT_API_URL/actions/release_or_reset_machines/runs" -d "$(jq -n --arg c "$name" \
    '{properties: {claim_name_or_hosts: $c, release_machines: true, reset_machines: false}}')" \
    | jq -c '{ok, run: .run.id}' || echo "::warning::could not release claim $name; its TTL frees the VMs"
}

name=${2:?usage: $0 acquire NAME N TTL | release NAME}
case $1 in
  acquire) n=${3:?usage: $0 acquire NAME N TTL} ttl=${4:?usage: $0 acquire NAME N TTL}; acquire ;;
  release) release ;;
  *) echo "usage: $0 acquire NAME N TTL | release NAME" >&2; exit 2 ;;
esac
