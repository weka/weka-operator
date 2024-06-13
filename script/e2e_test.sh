#!/usr/bin/env bash

set -euo pipefail
IFS=$'\n\t'

# Usage: e2e_test.sh --suite <suite_name> [--timeout <timeout>]

# Parse command line arguments
while [[ $# -gt 0 ]]; do
  key="$1"
  case $key in
  --suite)
    SUITE="$2"
    shift
    shift
    ;;
  --timeout)
    TIMEOUT="$2"
    shift
    shift
    ;;
  --bliss-version)
    BLISS_VERSION="$2"
    shift
    shift
    ;;
  --cluster-name)
    CLUSTER_NAME="$2"
    shift
    shift
    ;;
  --operator-version)
    OPERATOR_VERSION="$2"
    shift
    shift
    ;;
  --weka-image)
    WEKA_IMAGE="$2"
    shift
    shift
    ;;
  --)
    shift
    break
    ;;
  *)
    echo "Unknown option: $1"
    exit 1
    ;;
  esac
done

# Validate
if [[ -z "${SUITE+x}" ]]; then
  echo "Missing required argument: --suite"
  exit 1
fi

# Defaults
# Set default timeout
TIMEOUT="${TIMEOUT:-30m}"

ARGS=()

ARGS+=("-timeout=${TIMEOUT}")
ARGS+=("-run=^${SUITE}$")
ARGS+=("-count=1")
ARGS+=("-quay-username=${QUAY_USERNAME}")
ARGS+=("-quay-password=${QUAY_PASSWORD}")
ARGS+=("-cluster-name=${CLUSTER_NAME}")
ARGS+=("-bliss-version=${BLISS_VERSION:-latest}")
ARGS+=("-weka-image=${WEKA_IMAGE}")

if [[ -n "${OPERATOR_VERSION+x}" ]]; then
  ARGS+=("-operator-version=${OPERATOR_VERSION}")
fi

# Run the test SUITE
set -x
go test -v ./test "${ARGS[@]}" "$@"
