#!/usr/bin/env bash

set -euo pipefail
IFS=$'\n\t'

ROOT=$(git rev-parse --show-toplevel)
ANSIBLE_DIR="${ROOT}/ansible"

export ANSIBLE_CONFIG="${ANSIBLE_DIR}/ansible.cfg"
export ANSIBLE_INVENTORY="${ANSIBLE_DIR}/inventory.ini"

# Parse Arguments
while [[ $# -gt 0 ]]; do
  key="$1"
  case $key in
    -p | --profile)
      AWS_PROFILE="$2"
      shift
      shift
      ;;
    -t | --tags)
      TAGS="$2"
      shift
      shift
      ;;
    --skip-tags)
      SKIP_TAGS="$2"
      shift
      shift
      ;;
    --weka-version)
      WEKA_VERSION="$2"
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

# Build Ansible command
ANSIBLE_COMMAND=(poetry --directory "${ANSIBLE_DIR}" run ansible-playbook)

if [[ -n "${TAGS:-}" ]]; then
  ANSIBLE_COMMAND+=(--tags "${TAGS}")
fi

if [[ -n "${SKIP_TAGS:-}" ]]; then
  ANSIBLE_COMMAND+=(--skip-tags "${SKIP_TAGS}")
fi

if [[ -n "${WEKA_VERSION:-}" ]]; then
  ANSIBLE_COMMAND+=(-e weka_version="${WEKA_VERSION}")
fi

ANSIBLE_COMMAND+=(-e root="${ROOT}" "${ANSIBLE_DIR}/eks.yaml")

# Run ANSIBLE_COMMAND
"${ANSIBLE_COMMAND[@]}"
