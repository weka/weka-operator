#!/usr/bin/env bash
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

CONFIG=$(mktemp)
cat > "$CONFIG" <<'EOF'
processor:
  ignoreTypes:
    - ".*List$"
  ignoreFields:
    - "TypeMeta$"
    - "ObjectMeta$"
EOF

echo "==> Generating CRD reference docs..."
mkdir -p docs
go run github.com/elastic/crd-ref-docs@v0.3.0 \
  --source-path=./api/v1alpha1 \
  --config="$CONFIG" \
  --renderer=markdown \
  --output-path=docs/index.md
rm -f "$CONFIG"

echo "==> Generating kubectl explain snippets..."
python3 scripts/generate-kubectl-explain.py

echo "==> Splitting docs by resource..."
python3 scripts/split-crd-docs.py

echo "==> Setting up venv and installing mkdocs-material..."
uv venv --quiet .venv
source .venv/bin/activate
uv pip install -q mkdocs-material

echo "==> Serving docs at http://127.0.0.1:8000"
mkdocs serve
