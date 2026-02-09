#!/bin/bash
#
set -e

# Determine repository name based on branch
# Use production repository for any release/* branch, otherwise use -dev repository
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")

if [[ "$CURRENT_BRANCH" == release/* ]]; then
  export REPO="${REPO:-quay.io/weka.io/weka-operator}"
else
  export REPO="${REPO:-quay.io/weka.io/weka-operator-dev}"
fi

  kubectl run preload-"$(echo -n $VERSION | md5)" --image=$REPO:${VERSION} --restart=OnFailure \
    --overrides='{"spec":{"template":{"spec":{"nodeSelector":{"node-role.kubernetes.io/master":"true"}}}}}' \
    --command -- /bin/true

  kubectl wait --timeout=600s --for=jsonpath='{.status.phase}'=Succeeded pod/preload-"$(echo -n $VERSION | md5)"
