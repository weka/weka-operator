# Pod runtime CPU affinity

Semantics and caveats: [doc/operator/concepts/cpu-affinity.md](../doc/operator/concepts/cpu-affinity.md)

Runtime (`charts/weka-operator/resources/weka_runtime.py`):
- `setup_early_cpu_affinity` — first step of `main()`
- `get_support_cpus`, `get_remaining_cores`, `get_container_cpu_allocation` — support mask
- `get_pinned_agent_cmd` — agent under `taskset`
- `install_exec_shell_pinning`, `publish_support_cpus` — exec shells (`/tmp/weka-k8s-runtime/`)
- `periodic_cpu_affinity_management`, `manage_cpu_affinities` — 60s sweep
- `MODE_CORES_FLAG` — modes that get any of this

Go:
- `HasWekaCoresMode` / `IsBackendMode` — `pkg/weka-k8s-api/api/v1alpha1/container_types.go`
- `BASH_ENV` env — `internal/controllers/resources/pod.go`

Tests: `tests/runtime/test_cpu_affinity.py` (`make test-runtime`),
`pkg/weka-k8s-api/api/v1alpha1/container_types_test.go`
