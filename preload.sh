#!/bin/bash
#
set -e

export REPO="${REPO:-quay.io/weka.io/weka-operator}"

  kubectl run preload-"$(echo -n $VERSION | md5)" --image=$REPO:${VERSION} --restart=OnFailure \
    --overrides='{"spec":{"template":{"spec":{"nodeSelector":{"node-role.kubernetes.io/master":"true"}}}}}' \
    --command -- /bin/true

  kubectl wait --timeout=600s --for=jsonpath='{.status.phase}'=Succeeded pod/preload-"$(echo -n $VERSION | md5)"
