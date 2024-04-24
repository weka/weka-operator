#! /usr/bin/env bash

set -euo pipefail
IFS=$'\n\t'

# parse input arguments
while getopts ":t:" opt; do
  case ${opt} in
    t)
      TAG=$OPTARG
      ;;
    \?)
      echo "Usage: update_values.sh -t <tag>"
      exit 1
      ;;
  esac
done

ROOT=$(git rev-parse --show-toplevel)
FILE="${ROOT}/charts/weka-operator/values.yaml"

# Check for yq binary
if ! command -v yq &>/dev/null; then
  echo "yq could not be found. Please install yq before running this script."
  exit 1
fi

# Set image.tag
TAG="${TAG}" yq '.image.tag |= env(TAG)' -i "${FILE}"
