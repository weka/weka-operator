#!/usr/bin/env bash

set -euo pipefail
IFS=$'\n\t'

# Usage: cleanup-testing.sh [--remove-namespaces] [--remove-release]
#  --remove-namespaces: Remove the namespaces created for testing
#  --remove-release: Remove the helm release

REMOVE_NAMESPACES=false
REMOVE_RELEASE=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --remove-namespaces)
      REMOVE_NAMESPACES=true
      shift
      ;;
    --remove-release)
      REMOVE_RELEASE=true
      shift
      ;;
    *)
      echo "Unknown argument: $1"
      exit 1
      ;;
  esac
done

K=$(which kubectl)

NAMESPACES=(
  "default"
  "weka-operator-system"
  "weka-operator-e2e"
  "weka-operator-e2e-system"
)

for NAMESPACE in "${NAMESPACES[@]}"; do
  echo "${NAMESPACE}: Deleting Weka Clusters"
  "${K}" delete -n "${NAMESPACE}" wekacluster --all --ignore-not-found
  "${K}" wait --for=delete wekacluster --all

  echo "${NAMESPACE}: Deleting Weka Containers"
  "${K}" delete -n "${NAMESPACE}" wekacontainer --all --ignore-not-found
  "${K}" wait --for=delete wekacontainer --all

  echo "${NAMESPACE}: Deleting tombstones"
  "${K}" delete -n "${NAMESPACE}" tombstone --all --ignore-not-found
  "${K}" wait --for=delete tombstone --all

done

if [ "${REMOVE_RELEASE}" = true ]; then
  echo "Deleting Helm Release"
  helm uninstall --namespace weka-operator-e2e-system weka-operator --ignore-not-found
  helm uninstall --namespace weka-operator-system weka-operator --ignore-not-found

  # Delete Roles
  "${K}" delete -A clusterrole --selector 'app.kubernetes.io/part-of=weka-operator'
  "${K}" delete -A clusterrolebinding --selector 'app.kubernetes.io/part-of=weka-operator'
  "${K}" delete clusterrole weka-operator-manager-role --ignore-not-found
fi

if [[ "${REMOVE_NAMESPACES}" = "true" ]]; then
  echo "Deleting Namespaces"
  NAMESPACES=(
    "weka-operator-e2e"
    "weka-operator-e2e-system"
  )
  for NAMESPACE in "${NAMESPACES[@]}"; do
    echo "${NAMESPACE}: Deleting"
    "${K}" delete namespace "${NAMESPACE}" --ignore-not-found
  done
fi
