#!/usr/bin/env bash
# Used by the ci_gate.yaml upgrade-extended job. Looks at earlier attempts of job $JOB on commit
# $HEAD_SHA (all ci-gate runs and re-run attempts) and writes step outputs:
#   execution_id          - failed/interrupted execution to continue (empty: start from scratch;
#                           always empty with FRESH=true)
#   operator_image,
#   operator_helm_image   - operator already published for this commit
# Fails on any other status of the previous execution (running/pending: it still holds the lab).
# If another execution used the lab ($LAB_KUBE_CONTEXTS) since, starts from scratch instead of
# continuing.
set -euo pipefail

# curl, not gh: the self-hosted test runners have no gh.
api() {  # path jq-filter
  curl --fail --retry 3 --retry-delay 5 -sS -H "Authorization: Bearer $GH_TOKEN" \
    -H "Accept: application/vnd.github+json" "$GITHUB_API_URL/repos/$GITHUB_REPOSITORY/$1" | jq -r "$2"
}

# Completed, non-skipped attempts of this job on this commit, newest first.
runs=$(api "actions/workflows/ci_gate.yaml/runs?head_sha=$HEAD_SHA&event=pull_request&per_page=100" '.workflow_runs[].id')
jobs=""
for run in $runs; do
  j=$(api "actions/runs/$run/jobs?filter=all&per_page=100" \
    ".jobs[]|select(.name==\"$JOB\" and .status==\"completed\" and .conclusion!=\"skipped\")|\"\(.completed_at) \(.id) \(.conclusion)\"")
  jobs+="$j"$'\n'
done
jobs=$(sort -r <<< "$jobs" | sed '/^$/d')

newest() {  # prefix -> "job_id conclusion message" of the newest job with that notice annotation
  local id conclusion m
  while read -r _ id conclusion; do
    [[ -z "$id" ]] && continue
    # set -e does not reach into $(...): without the return a failed call would read as "none".
    m=$(api "check-runs/$id/annotations?per_page=100" "first(.[]|select(.message|startswith(\"$1\"))|.message) // empty") || return 1
    if [[ -n "$m" ]]; then echo "$id $conclusion $m"; return; fi
  done <<< "$jobs"
}

op=$(newest "Operator versions: ")
if [[ -n "$op" ]]; then
  read -r id _ _ _ img helm <<< "$op"
  echo "reusing operator published by job $id: $img $helm"
  echo "operator_image=$img" >> "$GITHUB_OUTPUT"
  echo "operator_helm_image=$helm" >> "$GITHUB_OUTPUT"
fi

ex=$(newest "Execution: ")
if [[ -z "$ex" ]]; then echo "no earlier $JOB execution on this commit: from scratch"; exit 0; fi
read -r id conclusion _ url <<< "$ex"
if [[ "$conclusion" == "success" ]]; then echo "job $id passed: from scratch"; exit 0; fi
EXECUTION_ID=${url##*/}
svc() {  # path jq-filter
  curl --fail --retry 3 --retry-delay 5 --retry-connrefused -sS "$WEKA_TESTING_SERVICE_URL/api/$1" | jq -r "$2"
}
x=$(svc "executions/$EXECUTION_ID" '"\(.status // "") \(.updated_at // "")"')
read -r STATUS UPDATED <<< "$x"
case "$STATUS" in
  succeeded) echo "execution $EXECUTION_ID succeeded: from scratch" ;;
  failed|interrupted)
    if [[ "$FRESH" == "true" ]]; then
      echo "fresh start requested: not continuing $STATUS execution $EXECUTION_ID"
      exit 0
    fi
    # Another run on the lab since X stopped (new or continued, hence updated_at) replaced what X
    # built. Timestamps compared to the second (the service writes varying fractional digits), so
    # >=: a same-second update counts as used.
    used=""
    for ctx in $LAB_KUBE_CONTEXTS; do
      used=$(svc "executions?kubeContext=$ctx" \
        "first((.executions // [])[]|select(.id != \"$EXECUTION_ID\" and (.updated_at // \"\")[:19] >= \"${UPDATED:0:19}\")|\"\(.id) (\(.flow_name), \(.status))\") // empty")
      if [[ -n "$used" ]]; then break; fi
    done
    if [[ -z "$used" ]]; then
      echo "execution $EXECUTION_ID is $STATUS: continuing it"
      echo "execution_id=$EXECUTION_ID" >> "$GITHUB_OUTPUT"
    else
      echo "lab used by execution $used after $EXECUTION_ID: from scratch"
    fi ;;
  *)
    echo "::error::previous execution $EXECUTION_ID of job $id is ${STATUS:-without status}, not continuing it"
    exit 1 ;;
esac
