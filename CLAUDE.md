# CLAUDE.md

This file provides guidance for AI agents working with this repository.

## AI Navigation System (.ainav)

The `.ainav/` directory contains a curated navigation map of the codebase. This is NOT source of truth (code is), but provides efficient navigation hints that should be consulted FIRST before exploring the codebase.

### How to Use .ainav

1. **Start at the entry point**: Read `.ainav/index.md` first
2. **Navigate by topic**: Follow links to subdirectory index files
3. **Max 3 hops**: Information is organized for quick access (index -> topic -> detail)

**Prefer `.ainav` first** when exploring or trying to understand the codebase. It often provides faster navigation with context. If the information there is insufficient, fall back to grep/glob/read as needed.

### .ainav Structure

```
.ainav/
  index.md              <- START HERE (entry point)
  controllers/
    index.md            <- Controller overview
    wekacontainer.md    <- Container lifecycle (most complex)
    wekacluster.md      <- Cluster lifecycle
    wekaclient.md       <- Client lifecycle
  operations/
    index.md            <- Operations, CSI, drivers
  config/
    index.md            <- Helm, env vars, API types
  services/
    index.md            <- Weka API, K8s utils, node-agent
```

### Keeping .ainav Updated

**IMPORTANT**: When making code changes, update relevant `.ainav` files if:
- Adding new files/directories that affect navigation
- Changing file purposes or locations
- Adding new features that should be documented

Files should stay under 3KB. If a file grows too large, split into subdirectories.
Primary index file allowed to grow larger (up to ~10KB) for comprehensive overview.

## Build Commands

```bash
# Run tests
make test

# Generate CRDs and manifests
make manifests

# Generate API documentation
make generate-api-docs

# Helm chart packaging
make helm-package
```

## Testing

```bash
# Unit tests
go test ./...

# Integration tests (requires cluster)
make test-e2e
```

## Code Style

- Go code follows standard Go conventions
- Never swallow errors unless explicitly asked for
- Controllers use `go-steps-engine` for step-based reconciliation
- Kubernetes resources use controller-runtime patterns
- Python code (in charts/resources) uses standard Python style

## Key Documentation

- `doc/summary.xml` - Documentation index with tags
- `doc/structure.md` - Documentation guidelines
- `doc/api_dump/` - Generated API reference (auto-generated, don't edit)

## Common Tasks

See `.ainav/tasks.md` for detailed guides on common development tasks.
