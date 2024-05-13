#!/usr/bin/env bash

set -euo pipefail
IFS=$'\n\t'

K=$(which kubectl)

NAMESPACES=(
  "default"
  "weka-operator-e2e"
  "weka-operator-e2e-system"
)

for NAMESPACE in "${NAMESPACES[@]}"; do
  echo "Namespace: ${NAMESPACE}"
  echo "Deleting Weka Clusters"
  CLUSTERS=$("${K}" get wekacluster -n "${NAMESPACE}" -o json | jq -r '.items[].metadata.name')
  for CLUSTER in ${CLUSTERS}; do
    "${K}" delete -n "${NAMESPACE}" wekacluster "${CLUSTER}" --ignore-not-found
  done
  "${K}" wait --for=delete wekacluster --all

  echo "Deleting Weka Containers"
  CONTAINERS=$("${K}" get wekacontainer -n "${NAMESPACE}" -o json | jq -r '.items[].metadata.name')
  for CONTAINER in ${CONTAINERS}; do
    "${K}" delete -n "${NAMESPACE}" wekacontainer "${CONTAINER}" --ignore-not-found
  done
  "${K}" wait --for=delete wekacontainer --all

  echo "Deleting tombstones"
  TOMBSTONES=$("${K}" get tombstone -n "${NAMESPACE}" -o json | jq -r '.items[].metadata.name')
  for TOMBSTONE in ${TOMBSTONES}; do
    "${K}" delete -n "${NAMESPACE}" tombstone "${TOMBSTONE}" --ignore-not-found
  done
  "${K}" wait --for=delete tombstone --all

done

helm uninstall --namespace weka-operator-e2e-system weka-operator --ignore-not-found
helm uninstall --namespace weka-operator-system weka-operator --ignore-not-found

# Delete Roles
"${K}" delete -A clusterrole --selector 'app.kubernetes.io/part-of=weka-operator'
"${K}" delete -A clusterrolebinding --selector 'app.kubernetes.io/part-of=weka-operator'
"${K}" delete clusterrole weka-operator-manager-role --ignore-not-found

echo "Deleting Namespaces"
NAMESPACES=(
  "weka-operator-e2e"
  "weka-operator-e2e-system"
)
for NAMESPACE in "${NAMESPACES[@]}"; do
  "${K}" delete namespace "${NAMESPACE}" --ignore-not-found
done
