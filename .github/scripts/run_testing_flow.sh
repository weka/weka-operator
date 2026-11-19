#!/usr/bin/env bash
# Used by drive_sharing.yaml. Subcommands against the testing service:
#   create  - POST $BODY as a new execution. Writes step output execution_id.
#   wait    - poll $EXECUTION_ID every 60 s until it succeeds; fails on any other final status,
#             after 5 consecutive unreadable polls, or after 120 minutes.
#   stop    - best-effort stop of $EXECUTION_ID; never fails.
# Env: WEKA_TESTING_SERVICE_URL, BODY, EXECUTION_ID.
set -euo pipefail

create() {
  local resp code
  # No retries: a retried POST the service already accepted gets a 409 and loses the execution id.
  resp=$(curl -sS -w "\n%{http_code}" \
    -X POST "$WEKA_TESTING_SERVICE_URL/api/executions" -H "Content-Type: application/json" -d "$BODY")
  code=$(tail -n1 <<< "$resp")
  resp=$(sed '$d' <<< "$resp")
  if [[ "$code" -lt 200 || "$code" -ge 300 ]]; then
    echo "::error::testing service POST /api/executions returned HTTP $code. Response: $resp"
    exit 1
  fi
  local id
  id=$(jq -r '.execution_id // empty' <<< "$resp")
  if [[ -z "$id" ]]; then
    echo "::error::testing service did not return execution_id. Response: $resp"
    exit 1
  fi
  echo "::notice::Execution: $WEKA_TESTING_SERVICE_URL/execution/$id"
  echo "execution_id=$id" >> "$GITHUB_OUTPUT"
}

wait_for() {
  local max_polls=120 max_empty=5 empty=0 status code rc i name=drive-sharing-regression
  local curl_err resp_body
  curl_err=$(mktemp); resp_body=$(mktemp)
  for ((i=1; i<=max_polls; i++)); do
    rc=0
    code=$(curl --max-time 30 --retry 3 --retry-delay 5 --retry-connrefused -sS -o "$resp_body" -w '%{http_code}' \
      "$WEKA_TESTING_SERVICE_URL/api/executions/$EXECUTION_ID" -H "Content-Type: application/json" 2>"$curl_err") || rc=$?
    # resp_body is reused across polls: a failed poll would otherwise re-read the last "running".
    # `// empty` so a missing/null .status yields "" instead of the string "null"
    status=""
    if [[ $rc -eq 0 && $code == 2?? ]]; then
      status=$(jq -r '.status // empty' < "$resp_body" 2>/dev/null || true)
    fi
    echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] [$i/$max_polls] Status: ${status:-<unreachable>}"
    case "$status" in
      succeeded) return 0 ;;
      running|pending) empty=0 ;;
      "")
        # a failed poll (network blip, bad gateway) is not a bad test state: tolerate a few in a row
        empty=$((empty + 1))
        echo "::warning::could not read execution status (attempt $empty/$max_empty) - retrying"
        echo "  curl exit=$rc http_code=${code:-none}"
        [[ -s "$curl_err" ]] && echo "  curl error: $(cat "$curl_err")"
        [[ -s "$resp_body" ]] && echo "  response body (truncated): $(head -c 300 "$resp_body")"
        if [[ $empty -ge $max_empty ]]; then
          echo "::error::testing service unreachable for $max_empty consecutive polls"
          exit 1
        fi ;;
      *) echo "::error::$name test failed with status: $status"; exit 1 ;;
    esac
    if [[ $i -eq $max_polls ]]; then
      echo "::error::$name test timed out after $max_polls minutes"
      exit 1
    fi
    sleep 60
  done
}

stop() {
  if [[ -z "${EXECUTION_ID:-}" ]]; then echo "no execution to stop"; return 0; fi
  # Bounded: on cancel, GitHub's short cleanup window must still fit the VM release after this.
  curl --fail --max-time 20 --retry 2 --retry-delay 5 -sS -X POST \
    "$WEKA_TESTING_SERVICE_URL/api/executions/$EXECUTION_ID/stop" \
    -H "Content-Type: application/json" \
    && echo "stopped execution $EXECUTION_ID" || echo "::warning::could not stop execution $EXECUTION_ID"
}

case "${1:-}" in
  create) create ;;
  wait) wait_for ;;
  stop) stop ;;
  *) echo "usage: $0 create|wait|stop" >&2; exit 2 ;;
esac
