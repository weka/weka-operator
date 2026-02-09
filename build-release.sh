#!/bin/bash


# This script creates usable release, can be ran locally or as part of workflow, whole release flow is encapsulated here
# what is not - github release create. This is done only via github-actions, by running this script and then semantic release, which will create tag and GH release, with release notes
# usually, to run locally you want to rely on VERSION=1.0.0-some-user-some-purpose.someiterationnum and not on semver count, as it will overwrite itself on each iteration
# VERSION=1.0.0-antontest.1 ./build-release.sh

set -ex

#npx semantic-release --dry-run # this  goes into github actions as separate step to set VERSION for this step
if [ -z "$VERSION" ]; then
  exit 1
fi
echo "Building version $VERSION"

# Determine repository names based on branch
# Use production repositories for any release/* branch, otherwise use -dev repositories
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
echo "Current branch: $CURRENT_BRANCH"

if [[ "$CURRENT_BRANCH" == release/* ]]; then
  echo "Building for production repositories (release/* branch)"
  export REPO="${REPO:-quay.io/weka.io/weka-operator}"
  export HELM_REPO="${HELM_REPO:-quay.io/weka.io/helm}"
else
  echo "Building for development repositories (non-release branch)"
  export REPO="${REPO:-quay.io/weka.io/weka-operator-dev}"
  export HELM_REPO="${HELM_REPO:-quay.io/weka.io/helm-dev}"
fi

echo "Using REPO: $REPO"
echo "Using HELM_REPO: $HELM_REPO"

#helm chart manipulations require to run go as well, and extracting needed parts from image is hell on GHA
#so if we need helm and go outside of the image, no reason to build binary within docker
#eventual reason might be caching, and well, repeatable environment, but then it needs to include helm as well,
#and it should be possible to build multiple artifacts with same cache, without duplicating dockerfiles

echo "Generating code and building binary, a local operation"
go generate ./...
make generate
make rbac
make crd
go vet ./...

echo "Building helm chart"
make chart VERSION=v$VERSION

# docker build here is merely packaging and uploading
echo "Building docker image and pushing"

# Build cache arguments - use GHA cache when running in GitHub Actions
CACHE_ARGS=""
if [ -n "$GITHUB_ACTIONS" ]; then
  echo "Running in GitHub Actions, using GHA cache"
  CACHE_ARGS="--cache-from type=gha --cache-to type=gha,mode=max"
fi

# Check if SSH_AUTH_SOCK is available, if not, build without SSH
if [ -z "$SSH_AUTH_SOCK" ] || [ ! -S "$SSH_AUTH_SOCK" ]; then
  echo "No SSH agent available, building without SSH"
  docker buildx build --platform linux/amd64 --tag $REPO:v$VERSION --push $CACHE_ARGS -f image.Dockerfile . || { echo "docker build failed, ensure login and re-run whole flow"; exit 1; }
else
  echo "SSH agent available, building with SSH"
  docker buildx build --ssh default --platform linux/amd64 --tag $REPO:v$VERSION --push $CACHE_ARGS -f image.Dockerfile . || { echo "docker build failed, ensure login and re-run whole flow"; exit 1; }
fi

# helm chart push
if ! helm push charts/weka-operator-*.tgz oci://$HELM_REPO; then
  echo "helm push failed, ensure login"
  rm -f charts/weka-operator-*.tgz
  exit 1
fi
rm -f charts/weka-operator-*.tgz

