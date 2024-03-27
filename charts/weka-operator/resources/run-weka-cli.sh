#!/bin/bash

set -o pipefail
set -e

export WEKA_USERNAME=`cat /var/run/secrets/weka-operator/operator-user/username`
export WEKA_PASSWORD=`cat /var/run/secrets/weka-operator/operator-user/password`
export WEKA_ORG=`cat /var/run/secrets/weka-operator/operator-user/org`

weka "$@"
