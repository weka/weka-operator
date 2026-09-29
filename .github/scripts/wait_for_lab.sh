#!/usr/bin/env bash
# Used by ci_gate.yaml test jobs. Waits until no kube context in $LAB_KUBE_CONTEXTS is in use on the
# testing service. Turnstyle only orders ci-gate runs; this also covers a clients-only re-run (which
# skips turnstyle) and executions started outside CI.
# Bounded: an execution orphaned by a dead runner keeps its context in use forever, and this job may
# already hold turnstyle, so every other PR would queue behind it.
set -euo pipefail
WAIT_MINUTES=180  # a clients-only re-run can wait out another PR's full upgrade-extended + clients-only

# --retry-all-errors: over a wait of hours, one 4xx blip (rate limit) must not drop the turnstyle slot.
svc() {  # path jq-filter
  curl --fail --retry 3 --retry-delay 5 --retry-all-errors -sS "$WEKA_TESTING_SERVICE_URL/api/$1" | jq -r "$2"
}

# A name the service does not report would never read as in use: fail instead of "lab free".
missing=$(svc kube-contexts '($ENV.LAB_KUBE_CONTEXTS|split(" ")|map(select(. != ""))) - [.contexts[].name]|join(" ")')
if [[ -n "$missing" ]]; then
  echo "::error::$WEKA_TESTING_SERVICE_URL/api/kube-contexts does not report: $missing"
  exit 1
fi

deadline=$((SECONDS + WAIT_MINUTES * 60))
while true; do
  busy=$(svc kube-contexts '[.contexts[]|select(.in_use and (.name|IN($ENV.LAB_KUBE_CONTEXTS|split(" ")[])))|.name]|join(" ")')
  if [[ -z "$busy" ]]; then echo "lab free: $LAB_KUBE_CONTEXTS"; exit 0; fi
  holders=""
  for ctx in $busy; do
    h=$(svc "executions?kubeContext=$ctx" \
      'first((.executions // [])[]|select(.status == "running" or .status == "pending")|"execution \(.id) (\(.flow_name), \(.status))") // "no running execution listed"')
    holders+="$ctx: $h; "
  done
  if (( SECONDS >= deadline )); then
    echo "::error::lab still in use after ${WAIT_MINUTES}m (${holders% }); stop it (POST /api/executions/{id}/stop) and re-run"
    exit 1
  fi
  echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] in use - ${holders% } waiting"
  sleep 30
done
