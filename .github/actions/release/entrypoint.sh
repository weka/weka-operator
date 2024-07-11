#!/usr/bin/env bash

set -euo pipefail
IFS=$'\n\t'

# GHA passess script args using positional parameters.  Inputs are passed as
# env vars, but these often containe invalid characters (ie. `-`).
GITHUB_TOKEN="${1}"
export GITHUB_TOKEN

BRANCH="${2}"
EVENT="${3}"

GIT_CLIFF=/usr/local/bin/git-cliff
echo "Git Cliff: $(${GIT_CLIFF} --version)"

SEMVER=/usr/local/bin/semver
echo "Semver: $(${SEMVER} --version)"

GH=/usr/local/bin/gh
echo "GH: $(${GH} --version)"

${GIT_CLIFF} --unreleased
${GIT_CLIFF} --unreleased --bumped-version

CURRENT_VERSION=$(git describe --tags --abbrev=0)
NEXT_VERSION=$(${GIT_CLIFF} --unreleased --bumped-version)
DIFF=$(semver diff "${CURRENT_VERSION}" "${NEXT_VERSION}")

echo "Current Version: ${CURRENT_VERSION}"

# If the branch is main, skip releasing for now
if [ "${BRANCH}" == "main" ]; then
  echo "Main branch, skipping release"
  exit 0
fi

# If the branch is a beta branch, release
# Betas are released from the release/v1-beta branch
if [[ "${BRANCH}" =~ ^release/v1-beta$ ]]; then
  echo "Beta branch, releasing"
  # Bump prerelease version
  NEXT_VERSION=$(semver bump prerelease "${NEXT_VERSION}")

  # Create a release
  # This is done as a prerelease for now it'll get promoted to a full release
  # once the binaries are built and published
  ${GH} release create "${NEXT_VERSION}" --title "${NEXT_VERSION}" --notes "Release ${NEXT_VERSION}" --prerelease
  exit 0
fi

# If the branch is a release branch, release
if [[ "${BRANCH}" =~ ^release/.*$ ]]; then
  echo "Release branch, releasing"
  # Bump release version, removing the prerelease qualifiers if present
  NEXT_VERSION=$(semver bump release "${NEXT_VERSION}")

  # Create a release
  # This is done as a prerelease for now it'll get promoted to a full release
  # once the binaries are built and published
  ${GH} release create "${NEXT_VERSION}" --title "${NEXT_VERSION}" --notes "Release ${NEXT_VERSION}" --prerelease
  exit 0
fi

# If the branch is is part of a PR, look for a release label
# If the PR has a release label, release, otherwise skip
if gh pr view --json 'id' >/dev/null; then
  echo "PR found"
  PR_LABELS=$(gh pr view --json labels --jq '.labels[].name')
  echo "PR Labels: ${PR_LABELS}"
  if ! echo "${PR_LABELS}" | grep -q "release"; then
    echo "Release label not found"
    exit 0
  fi

  echo "Release label found"
  # Chop refs/heads/ from the branch name
  BRANCH=${BRANCH#refs/heads/}
  NEXT_VERSION=$(semver bump build "${BRANCH//[^[:alnum:]]/.}" "${NEXT_VERSION}")
  ${GH} release create "${NEXT_VERSION}" --title "${NEXT_VERSION}" --notes "Release ${NEXT_VERSION}" --prerelease
fi
