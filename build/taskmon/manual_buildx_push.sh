#!/bin/zsh

set -e

if [[ $# -ne 2 ]]; then
    echo "Usage: $0 <base_image> <wekapp_commit_hash>"
    exit 1
fi

BASE_IMAGE=$1
WEKAPP_COMMIT_HASH=$2
ARCHITECTURE="x86_64"

TARGET_IMAGE="quay.io/weka.io/taskmon:${WEKAPP_COMMIT_HASH}_${ARCHITECTURE}"

docker buildx build --build-arg BASE_IMAGE="$BASE_IMAGE" --push --platform linux/amd64 -t "$TARGET_IMAGE" .

echo "$TARGET_IMAGE is built"
