#!/usr/bin/env bash

set -euo pipefail
IFS=$'\n\t'

# Usage: e2e_test.sh --suite <suite_name>

# Parse command line arguments
while [[ $# -gt 0 ]]; do
  key="$1"
  case $key in
    --suite)
      SUITE="$2"
      shift
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

# Run the test SUITE
go test -v ./test/e2e -run "${SUITE}"
