#!/usr/bin/env bash

set -euo pipefail
IFS=$'\n\t'

source "$(dirname "${BASH_SOURCE[0]}")/ansible_runner.sh"

# Parse Arguments
while [[ $# -gt 0 ]]; do
  key="$1"
  case $key in
    -p | --profile)
      AWS_PROFILE="$2"
      shift
      shift
      ;;
    *)
      echo "Unknown option: $1"
      exit 1
      ;;
  esac
done

# Set AWS_PROFILE
AWS_PROFILE=${AWS_PROFILE:-devkube}
export AWS_PROFILE

ANSIBLE_COMMAND+=("${ANSIBLE_DIR}/eks.yaml")

# Run ANSIBLE_COMMAND
"${ANSIBLE_COMMAND[@]}"
