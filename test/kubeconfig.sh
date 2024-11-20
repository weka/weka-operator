#!/usr/bin/env bash

set -euo pipefail
IFS=$'\n\t'

if [ -n "${KUBECONFIG:-}" ]; then
    echo "$KUBECONFIG"
else
    ls -1tr ~/kube-* | tail -n1
fi
