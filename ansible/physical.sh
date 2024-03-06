#!/usr/bin/env bash

set -euo pipefail
IFS=$'\n\t'

source "$(dirname "${BASH_SOURCE[0]}")/ansible_runner.sh"

ANSIBLE_COMMAND+=("${ANSIBLE_DIR}/physical.yaml")

# Run ANSIBLE_COMMAND
"${ANSIBLE_COMMAND[@]}"
