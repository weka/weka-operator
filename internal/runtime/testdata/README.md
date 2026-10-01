# Synthetic test fixtures

All files in this directory are **synthetic** — hand-written to match the JSON
shapes produced/consumed by the operator and `weka_runtime.py`/Go runtime.
None contain real cluster, customer, or drive data.

- `resources.json` — `weka.ContainerAllocations` written by the operator to
  `/opt/weka/k8s-runtime/resources.json`.
- `shutdown_instructions_*.json` — `domain.ShutdownInstructions` variants
  written to `.../shutdown_instructions.json`.
- `release_*.spec` — release spec files under `/opt/weka/dist/release/`
  (`{"feature_flags": "<base64 bitmap>"}`).
- `weka-resources.json` — Weka-owned resource document
  (`weka-resources.<gen>.json`) with nested unknown fields and an integer
  above 2^53, to exercise document-preserving (`json.RawMessage`) handling.
- `driver_loader_results.json`, `discovery_results.json` — `results.json`
  written by drivers-loader/discovery modes.
