package weka

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	"github.com/weka/weka-operator/internal/runtime/config"
)

// EnsureStemContainer creates a minimal "stem" Weka container if it doesn't already exist.
// Mirrors Python ensure_stem_container() at weka_runtime.py:3019.
func EnsureStemContainer(ctx context.Context, name string, port int) error {
	script := fmt.Sprintf(`
if [ -d /driver-toolkit-shared ]; then
    mkdir -p /lib/modules
    mkdir -p /usr/src
    mount -o bind /driver-toolkit-shared/lib/modules /lib/modules
    mount -o bind /driver-toolkit-shared/usr/src /usr/src
fi
weka local ps | grep %s || weka local setup container --name %s --net udp --base-port %d --no-start --disable
`, name, name, port)
	return cmdutil.Run(ctx, "sh", "-c", script)
}

// StartStemContainer starts the stem container by running "weka local start" detached.
// weka local start does not return, so it must run as a background process.
// Mirrors Python start_stem_container() at weka_runtime.py:3041.
func StartStemContainer(_ context.Context) error {
	cmd := exec.Command("weka", "local", "start") //nolint:gosec // hardcoded weka binary path
	return cmd.Start()
}

// EnsureContainerExec polls until the named container accepts exec commands.
// Polls every 1s with a 300s total timeout.
// Mirrors Python ensure_container_exec() at weka_runtime.py:3055.
func EnsureContainerExec(ctx context.Context, name string) error {
	deadline := time.Now().Add(300 * time.Second)
	for {
		err := cmdutil.Run(ctx, "weka", "local", "exec", "--container", name, "--", "ls")
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("container %q not exec-ready after 5 minutes: %w", name, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}

// ConfigureTraces writes or removes the trace dumper config inside the named container.
// Mirrors Python configure_traces() at weka_runtime.py:2379.
func ConfigureTraces(ctx context.Context, cfg *config.Config, name string) error {
	mode := cfg.DumperConfigMode
	if mode == "auto" || mode == "" {
		if cfg.Features.TracesOverridePartialSupport {
			mode = "partial-override"
		} else {
			mode = "cluster"
		}
	}

	const (
		oldFullLocation    = "/data/reserved_space/dumper_config.json.override"
		legacyPartialLoc   = "/data/reserved_space/dumper_config_overrides.json"
		newPartialLoc      = "/traces/config_overrides.json"
		stagingPath        = "/opt/weka/k8s-scripts/dumper_config.json.override"
	)

	switch mode {
	case "override":
		data := map[string]interface{}{
			"enabled":               true,
			"ensure_free_space_bytes": cfg.EnsureFreeSpaceGB * 1024 * 1024 * 1024,
			"retention_bytes":       cfg.MaxTraceCapacityGB * 1024 * 1024 * 1024,
			"retention_type":        "BYTES",
			"version":               1,
			"freeze_period": map[string]interface{}{
				"start_time": "0001-01-01T00:00:00+00:00",
				"end_time":   "0001-01-01T00:00:00+00:00",
				"retention":  0,
			},
		}
		return writeConfigToContainer(ctx, name, data, stagingPath, oldFullLocation)

	case "partial-override":
		data := map[string]interface{}{
			"ensure_free_space_bytes": cfg.EnsureFreeSpaceGB * 1024 * 1024 * 1024,
			"retention_bytes":       cfg.MaxTraceCapacityGB * 1024 * 1024 * 1024,
			"retention_type":        "BYTES",
		}
		dest := legacyPartialLoc
		if cfg.Features.TracesOverrideInSlashTraces {
			dest = newPartialLoc
		}
		if err := writeConfigToContainer(ctx, name, data, stagingPath, dest); err != nil {
			return err
		}

	case "cluster":
		script := fmt.Sprintf("weka local run --container %s rm -f %s %s %s",
			name, oldFullLocation, legacyPartialLoc, newPartialLoc)
		if err := cmdutil.Run(ctx, "sh", "-c", script); err != nil {
			return fmt.Errorf("configure_traces cluster mode: %w", err)
		}

	default:
		return fmt.Errorf("invalid DUMPER_CONFIG_MODE: %q", mode)
	}

	if cfg.Mode == "ssdproxy" {
		ensureFreeBytes := 0
		if mode == "partial-override" || mode == "override" {
			ensureFreeBytes = cfg.EnsureFreeSpaceGB * 1024 * 1024 * 1024
		}
		ssdCfg := map[string]interface{}{
			"enabled":               true,
			"ensure_free_space_bytes": ensureFreeBytes,
			"freeze_period": map[string]interface{}{
				"comment":    "",
				"end_time":   "1970-01-01T00:00:00Z",
				"retention":  0,
				"start_time": "1970-01-01T00:00:00Z",
			},
			"retention_type": "DEFAULT",
			"version":        1,
			"weka_iops_rate": map[string]interface{}{},
		}
		if err := writeConfigToContainer(ctx, name, ssdCfg, "/opt/weka/k8s-scripts/config.json", "/traces/config.json"); err != nil {
			return fmt.Errorf("configure_traces ssdproxy config.json: %w", err)
		}
	}

	return nil
}

func writeConfigToContainer(ctx context.Context, name string, data map[string]interface{}, staging, dest string) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	script := fmt.Sprintf(`set -e
mkdir -p /opt/weka/k8s-scripts
echo '%s' > %s
weka local run --container %s mv %s %s`, string(b), staging, name, staging, dest)
	if err := cmdutil.Run(ctx, "sh", "-c", script); err != nil {
		return fmt.Errorf("configure_traces write to container: %w", err)
	}
	return nil
}
