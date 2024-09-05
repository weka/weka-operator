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
  --verbose)
    VERBOSE=true
    shift
    ;;
  --debug)
    DEBUG=true
    shift
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
TIMEOUT="${TIMEOUT:-20m}"

# Run the test SUITE
set -x
go test -v ./test -timeout "${TIMEOUT}" -run "^${SUITE}$" -count 1 -failfast \
  -bliss-version "${BLISS_VERSION}" -verbose "${VERBOSE:-false}" -debug "${DEBUG:-false}"
