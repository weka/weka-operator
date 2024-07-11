#!/usr/bin/env bash

set -euo pipefail
IFS=$'\n\t'

# GHA passess script args using positional parameters.  Inputs are passed as
# env vars, but these often containe invalid characters (ie. `-`).
GITHUB_TOKEN="${1}"
export GITHUB_TOKEN

BRANCH="${2}"

GIT_CLIFF=/usr/local/bin/git-cliff
echo "Git Cliff: $(${GIT_CLIFF} --version)"

SEMVER=/usr/local/bin/semver
echo "Semver: $(${SEMVER} --version)"

set -x

whoami
ls -al .

${GIT_CLIFF} --unreleased
${GIT_CLIFF} --unreleased --bumped-version

CURRENT_VERSION=$(git describe --tags --abbrev=0)
NEXT_VERSION=$(${GIT_CLIFF} --unreleased --bumped-version)
DIFF=$(semver diff "${CURRENT_VERSION}" "${NEXT_VERSION}")

echo "Current Version: ${CURRENT_VERSION}"
echo "Next Version: ${NEXT_VERSION}"
echo "Diff: ${DIFF}"
