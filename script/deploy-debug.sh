#!/usr/bin/env bash
# Run the in-cluster operator manager under Delve.
#
#   ./script/deploy-debug.sh            build, push, and switch the Deployment to dlv
#   ./script/deploy-debug.sh restore    roll the Deployment back to the previous revision
#
# Env: KUBECONFIG, REPO, TAG, NAMESPACE, DEPLOYMENT, GOARCH. See doc/dev/debugging-with-delve.md.
set -euo pipefail

readonly REPO="${REPO:-quay.io/weka.io/weka-operator-dev}"
readonly NAMESPACE="${NAMESPACE:-weka-operator-system}"
readonly DEPLOYMENT="${DEPLOYMENT:-weka-operator-controller-manager}"
GOARCH="${GOARCH:-amd64}"

restore() {
  kubectl -n "$NAMESPACE" rollout undo deployment "$DEPLOYMENT"
  kubectl -n "$NAMESPACE" rollout status deployment "$DEPLOYMENT" --timeout=300s
}

deploy() {
  local root tag
  root="$(git rev-parse --show-toplevel)"
  tag="${TAG:-debug-$(whoami)-$(git -C "$root" rev-parse --short HEAD)-$(date +%Y%m%d%H%M%S)}"
  cd "$root"

  printf '==> building %s debug binary\n' "$GOARCH"
  CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build -gcflags="all=-N -l" -o bin/weka-operator-debug ./cmd/manager/main.go

  printf '==> building and pushing %s:%s\n' "$REPO" "$tag"
  docker buildx build --platform "linux/$GOARCH" -f debug.Dockerfile -t "$REPO:$tag" --push bin

  printf '==> switching %s/%s to dlv\n' "$NAMESPACE" "$DEPLOYMENT"
  # One patch = one ReplicaSet revision, so `restore` (rollout undo) returns to the exact previous state.
  # No probes and no leader election, so a paused breakpoint doesn't kill the manager (see the doc).
  kubectl -n "$NAMESPACE" patch deployment "$DEPLOYMENT" --type=strategic -p "$(cat <<JSON
{"spec":{"template":{"spec":{"containers":[{
  "name":"manager",
  "image":"$REPO:$tag",
  "command":["/usr/local/bin/dlv","exec","/weka-operator","--headless","--listen=127.0.0.1:2345",
             "--api-version=2","--accept-multiclient","--continue","--"],
  "livenessProbe":null,
  "readinessProbe":null,
  "env":[{"name":"ENABLE_LEADER_ELECTION","value":"false"}]
}]}}}}
JSON
)"
  kubectl -n "$NAMESPACE" rollout status deployment "$DEPLOYMENT" --timeout=300s

  printf '\nAttach:  kubectl -n %s port-forward deploy/%s 2345:2345\n' "$NAMESPACE" "$DEPLOYMENT"
  printf 'Then connect a Go Remote debugger to localhost:2345.\n'
  printf 'Restore: %s restore\n' "$0"
}

case "${1:-deploy}" in
  deploy) deploy ;;
  restore) restore ;;
  *) printf 'usage: %s [deploy|restore]\n' "$0" >&2; exit 2 ;;
esac
