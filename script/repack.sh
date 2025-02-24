#!/bin/zsh
#
set -e

export SOURCE_REPO=${SOURCE_REPO:-quay.io/weka.io/weka-in-container}

docker buildx build --platform linux/amd64 --tag $REPO:$VERSION --push --build-arg SOURCE=$SOURCE_REPO:$VERSION  -f repack.Dockerfile .
