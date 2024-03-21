#!/usr/bin/env bash

set -euo pipefail
IFS=$'\n\t'

ROOT=$(git rev-parse --show-toplevel)
ANSIBLE_DIR="${ROOT}/ansible"
WEKAPP_DIR=$(realpath "${ROOT}/../wekapp")

export ANSIBLE_CONFIG="${ANSIBLE_DIR}/ansible.cfg"
export ANSIBLE_INVENTORY="${ANSIBLE_DIR}/inventory.ini"

# Silence failures relating to constantize string in lookups
export OBJC_DISABLE_INITIALIZE_FORK_SAFETY=YES

# Parse Arguments
while [[ $# -gt 0 ]]; do
  key="$1"
  case $key in
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

ANSIBLE_COMMAND+=(
  -e root="${ROOT}"
  -e wekapp_root="${WEKAPP_DIR}"
  "${ANSIBLE_DIR}/oci.yaml"
)

echo "Running Ansible command: ${ANSIBLE_COMMAND[*]}"

# Run ANSIBLE_COMMAND
"${ANSIBLE_COMMAND[@]}"
