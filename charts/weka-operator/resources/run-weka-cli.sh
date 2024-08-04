#!/bin/bash

set -o pipefail
set -e

export WEKA_USERNAME=`cat /var/run/secrets/weka-operator/operator-user/username`
export WEKA_PASSWORD=`cat /var/run/secrets/weka-operator/operator-user/password`
export WEKA_ORG=`cat /var/run/secrets/weka-operator/operator-user/org`
# comes either out of pod spec on repeat run or from resources.json on first run
if [[ "$PORT" == "0" ]]; then
  export WEKA_PORT=`cat /opt/weka/k8s-runtime/vars/port`
  export PORT=`cat /opt/weka/k8s-runtime/vars/port`
fi

if [[ "$AGENT_PORT" == "0" ]]; then
  export AGENT_PORT=`cat /opt/weka/k8s-runtime/vars/agent_port`
fi


/usr/bin/weka "$@"
