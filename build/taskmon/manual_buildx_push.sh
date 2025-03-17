#!/bin/zsh

set -e

BASE_IMAGE=f8a10f04603cfddb_x86_64
VERSION=0.0.18

TARGET_IMAGE=quay.io/weka.io/taskmon:$VERSION-$BASE_IMAGE

docker buildx build --build-arg BASE_IMAGE=$BASE_IMAGE --push --platform linux/amd64 -t $TARGET_IMAGE .

echo $TARGET_IMAGE is built
