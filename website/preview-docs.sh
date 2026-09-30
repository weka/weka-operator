#!/usr/bin/env bash
# Usage: preview-docs.sh [--build-only]
# Default: generate, then serve at http://127.0.0.1:8000 from a local venv.
# --build-only: generate and `mkdocs build --strict` (mkdocs-material must be installed).
set -euo pipefail

case "${1:-}" in
  ""|--build-only) ;;
  *) echo "usage: $0 [--build-only]" >&2; exit 2 ;;
esac

cd "$(dirname "${BASH_SOURCE[0]}")"

CONFIG=$(mktemp)
trap 'rm -f "$CONFIG"' EXIT
cat > "$CONFIG" <<'YAML'
processor:
  ignoreTypes:
    - ".*List$"
  ignoreFields:
    - "TypeMeta$"
    - "ObjectMeta$"
YAML

echo "==> Generating CRD reference docs..."
mkdir -p docs
rm -f docs/*.md
OUTPUT_PATH="$PWD/docs/index.md"
# crd-ref-docs needs the nested Go module as cwd.
(cd ../pkg/weka-k8s-api && go run github.com/elastic/crd-ref-docs@v0.3.0 \
  --source-path=./api/v1alpha1 \
  --config="$CONFIG" \
  --renderer=markdown \
  --output-path="$OUTPUT_PATH")

echo "==> Generating kubectl explain snippets..."
python3 scripts/generate-kubectl-explain.py

echo "==> Splitting docs by resource..."
python3 scripts/split-crd-docs.py

echo "==> Copying Helm chart reference..."
cp ../charts/weka-operator/README.md docs/helm-chart.md

if [[ "${1:-}" == "--build-only" ]]; then
  mkdocs build --strict
  exit 0
fi

echo "==> Setting up venv and installing mkdocs-material..."
uv venv --quiet .venv
source .venv/bin/activate
uv pip install -q mkdocs-material==9.7.7

echo "==> Serving docs at http://127.0.0.1:8000"
mkdocs serve
