#!/usr/bin/env bash

set -euo pipefail
IFS=$'\n\t'

# (export NAMESPACE=weka-operator-e2e-system ; k -n $NAMESPACE logs -f $(k -n $NAMESPACE get pod -o json | jq -r '.items[] .metadata | select(.name| startswith("weka-operator")) | .name'))

# defaults
NAMESPACE="default"

# Parse Arguments
while getopts ":n:" opt; do
  case ${opt} in
    n)
      NAMESPACE=$OPTARG
      ;;
    \?)
      echo "Usage: stream-logs.sh -n <namespace>"
      exit 1
      ;;
    :)
      echo "Invalid option: $OPTARG requires an argument"
      exit 1
      ;;
  esac
done

K=$(which kubectl)

POD=$("${K}" -n "${NAMESPACE}" get pod -o json |
  jq -r '.items[] .metadata | select(.name| startswith("weka-operator")) | .name')

if [ -z "$POD" ]; then
  echo "No Weka Operator pods found in namespace: ${NAMESPACE}"
  exit 1
fi

echo "Streaming logs for Weka Operator pods in namespace: ${NAMESPACE}"
"${K}" --namespace="${NAMESPACE}" logs --follow=true "${POD}"
