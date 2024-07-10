#!/usr/bin/env bash

set -euo pipefail
IFS=$'\n\t'

# GHA passess script args using positional parameters.  Inputs are passed as
# env vars, but these often containe invalid characters (ie. `-`).
GITHUB_TOKEN="${1}"
export GITHUB_TOKEN

BRANCH="${2}"

SEMANTIC_RELEASE=/usr/local/bin/semantic-release
echo "Go Semantic Release: $(${SEMANTIC_RELEASE} --version)"

set -x

"${SEMANTIC_RELEASE}" --token "${GITHUB_TOKEN}" \
  --provider-opt slug=weka/weka-operator \
  --hooks goreleaser \
  --version-file \
  --dry \
  --prerelease \
  --match "$(jq -r '.match' <.semrelrc)"
