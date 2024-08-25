#!/bin/zsh


# This script creates usable release, can be ran locally or as part of workflow, whole release flow is encapsulated here
# what is not - github release create. This is done only via github-actions, by running this script and then semantic release, which will create tag and GH release, with release notes
# usually, to run locally you want to rely on VERSION=1.0.0-some-user-some-purpose.someiterationnum and not on semver count, as it will overwrite itself on each iteration
# VERSION=1.0.0-antontest.1 ./build-release.sh

set -e

#npx semantic-release --dry-run # this  goes into github actions as separate step to set VERSION for this step
if [ -z "$VERSION" ]; then
  exit 1
fi
echo "Building version $VERSION"

#helm chart manipulations require to run go as well, and extracting needed parts from image is hell on GHA
#so if we need helm and go outside of the image, no reason to build binary within docker
#eventual reason might be caching, and well, repeatable environment, but then it needs to include helm as well,
#and it should be possible to build multiple artifacts with same cache, without duplicating dockerfiles
export CGO_ENABLED=0
export GOOS=linux
# multi-arch should be simple, but, weka image not packaged yet for arm, and is messy, so omitting for now
export GOARCH=amd64

echo "Generating code and building binary"
make generate
make rbac
make crd
go vet ./...
go build -o dist/weka-operator cmd/manager/main.go

echo "Building helm chart"
make chart VERSION=$VERSION

# docker build here is merely packaging and uploading
echo "Building docker image and pushing"
docker buildx build --platform linux/amd64 --tag quay.io/weka.io/weka-operator:$VERSION --push -f image.Dockerfile . || echo "docker build failed, ensure login and re-run whole flow"

# helm chart push
if ! helm push charts/weka-operator-*.tgz oci://quay.io/weka.io/helm; then
  echo "helm push failed, ensure login"
  rm -f charts/weka-operator-*.tgz
  exit 1
fi
rm -f charts/weka-operator-*.tgz

