#!/bin/bash


# This script creates usable release, can be ran locally or as part of workflow, whole release flow is encapsulated here
# what is not - github release create. This is done only via github-actions, by running this script and then semantic release, which will create tag and GH release, with release notes
# usually, to run locally you want to rely on VERSION=1.0.0-some-user-some-purpose.someiterationnum and not on semver count, as it will overwrite itself on each iteration
# VERSION=1.0.0-antontest.1 ./build-release.sh
# set BUILD_OPERATOR_IMAGE=false or BUILD_POD_RUNTIME_IMAGE=false to skip that image; the chart is pushed only with the operator image

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
  export REPO_POD_RUNTIME="${REPO_POD_RUNTIME:-quay.io/weka.io/weka-pod-runtime}"
  export HELM_REPO="${HELM_REPO:-quay.io/weka.io/helm}"
else
  echo "Building for development repositories (non-release branch)"
  export REPO="${REPO:-quay.io/weka.io/weka-operator-dev}"
  export REPO_POD_RUNTIME="${REPO_POD_RUNTIME:-quay.io/weka.io/weka-pod-runtime-dev}"
  export HELM_REPO="${HELM_REPO:-quay.io/weka.io/helm-dev}"
fi

BUILD_OPERATOR_IMAGE="${BUILD_OPERATOR_IMAGE:-true}"
BUILD_POD_RUNTIME_IMAGE="${BUILD_POD_RUNTIME_IMAGE:-true}"

echo "Using REPO: $REPO"
echo "Using REPO_POD_RUNTIME: $REPO_POD_RUNTIME"
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

# The chart is released together with the operator image it references
if [ "$BUILD_OPERATOR_IMAGE" = "true" ]; then
  echo "Building helm chart"
  make chart VERSION=v$VERSION

  # Re-package chart with the correct image repository for the target registry
  CHART_TMP=$(mktemp -d)
  cp -r charts/weka-operator "$CHART_TMP/"
  # Use portable sed syntax that works on both BSD (macOS) and GNU (Linux)
  if [[ "$OSTYPE" == "darwin"* ]]; then
    sed -i '' -e "s|repository: quay.io/weka.io/weka-operator.*|repository: $REPO|" \
      -e "s|repository: quay.io/weka.io/weka-pod-runtime$|repository: $REPO_POD_RUNTIME|" "$CHART_TMP/weka-operator/values.yaml"
  else
    sed -i -e "s|repository: quay.io/weka.io/weka-operator.*|repository: $REPO|" \
      -e "s|repository: quay.io/weka.io/weka-pod-runtime$|repository: $REPO_POD_RUNTIME|" "$CHART_TMP/weka-operator/values.yaml"
  fi
  helm package "$CHART_TMP/weka-operator" --destination charts --version v$VERSION
  rm -rf "$CHART_TMP"
fi

# docker build here is merely packaging and uploading
echo "Building docker images and pushing"

# Build cache arguments - use GHA cache when running in GitHub Actions, one scope per image
# so the two builds don't evict each other's cache
OPERATOR_CACHE_ARGS=""
POD_RUNTIME_CACHE_ARGS=""
if [ -n "$GITHUB_ACTIONS" ]; then
  echo "Running in GitHub Actions, using GHA cache"
  OPERATOR_CACHE_ARGS="--cache-from type=gha,scope=operator --cache-to type=gha,scope=operator,mode=max"
  POD_RUNTIME_CACHE_ARGS="--cache-from type=gha,scope=pod-runtime --cache-to type=gha,scope=pod-runtime,mode=max"
fi

# Git credentials for private module fetch, passed as build secrets so they stay out of
# image layers and out of the cache exported by --cache-to mode=max. In CI the release job
# writes token-bearing copies to the home dir; locally the committed gitconfig rewrites
# fetches to SSH so that the agent forwarded by --ssh is used instead.
SECRET_ARGS=""
if [ -n "$GITHUB_ACTIONS" ]; then
  GITCONFIG_SRC="$HOME/.gitconfig"
else
  GITCONFIG_SRC="dockerfile_files/.gitconfig"
fi
if [ -f "$GITCONFIG_SRC" ]; then
  SECRET_ARGS="$SECRET_ARGS --secret id=gitconfig,src=$GITCONFIG_SRC"
else
  echo "WARNING: no gitconfig at $GITCONFIG_SRC, private module fetch may fail"
fi
if [ -f "$HOME/.netrc" ]; then
  SECRET_ARGS="$SECRET_ARGS --secret id=netrc,src=$HOME/.netrc"
fi

# Check if SSH_AUTH_SOCK is available, if not, build without SSH
SSH_ARGS=""
if [ -z "$SSH_AUTH_SOCK" ] || [ ! -S "$SSH_AUTH_SOCK" ]; then
  echo "No SSH agent available, building without SSH"
else
  echo "SSH agent available, building with SSH"
  SSH_ARGS="--ssh default"
fi

if [ "$BUILD_OPERATOR_IMAGE" = "true" ]; then
  docker buildx build $SSH_ARGS --platform linux/amd64,linux/arm64 --tag $REPO:v$VERSION --push $OPERATOR_CACHE_ARGS $SECRET_ARGS -f image.Dockerfile . || { echo "docker build failed, ensure login and re-run whole flow"; exit 1; }
fi

# Tag must equal the chart version: the chart defaults the pod runtime image tag to .Chart.Version
if [ "$BUILD_POD_RUNTIME_IMAGE" = "true" ]; then
  docker buildx build $SSH_ARGS --platform linux/amd64,linux/arm64 --tag $REPO_POD_RUNTIME:v$VERSION --build-arg VERSION=v$VERSION --push $POD_RUNTIME_CACHE_ARGS $SECRET_ARGS -f pod-runtime.Dockerfile . || { echo "docker build failed, ensure login and re-run whole flow"; exit 1; }
fi

# helm chart push
if [ "$BUILD_OPERATOR_IMAGE" = "true" ]; then
  if ! helm push charts/weka-operator-*.tgz oci://$HELM_REPO; then
    echo "helm push failed, ensure login"
    rm -f charts/weka-operator-*.tgz
    exit 1
  fi
  rm -f charts/weka-operator-*.tgz
fi

