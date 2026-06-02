// Package agent configures and manages the weka-agent process.
// Mirrors configure_agent, get_agent_cmd, await_agent, ensure_drivers, override_dependencies_flag
// at weka_runtime.py:1208–3128.
package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/osinfo"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/drivers"
)

// Configure patches /etc/wekaio/service.conf and writes /etc/wekaio/service.json.
// handleDrivers=false means agent should NOT handle drivers (compute/drive/client pass false).
// Mirrors Python configure_agent() at weka_runtime.py:2924.
func Configure(ctx context.Context, cfg *config.Config, handleDrivers bool) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "agent.Configure")
	defer logger.End()

	ignoreDriverFlag := "true"
	if handleDrivers {
		ignoreDriverFlag = "false"
	}

	expandConditionMounts := ""
	if cfg.Mode == "s3" || cfg.Mode == "envoy" {
		expandConditionMounts = ",envoy-data"
	}

	skipEnvoySetup := ""
	if cfg.Mode == "s3" {
		skipEnvoySetup = "sed -i 's/skip_envoy_setup=.*/skip_envoy_setup=true/g' /etc/wekaio/service.conf || true"
	}

	// M5: Envoy agent env vars.
	// Mirrors Python configure_agent() at weka_runtime.py:2934-2936:
	//   if MODE == "envoy":
	//       env_vars['RESTART_EPOCH_WANTED'] = str(int(os.environ.get("envoy_restart_epoch", time.time())))
	//       env_vars['BASE_ID'] = PORT
	envoyEnvExports := ""
	if cfg.Mode == "envoy" {
		restartEpoch := os.Getenv("envoy_restart_epoch")
		if restartEpoch == "" {
			restartEpoch = fmt.Sprintf("%d", time.Now().Unix())
		}
		envoyEnvExports = fmt.Sprintf("export RESTART_EPOCH_WANTED=%s\nexport BASE_ID=%d\n",
			restartEpoch, cfg.Port)
	}

	script := fmt.Sprintf(`%s
CONFFILE="/etc/wekaio/service.conf"
PATTERN="skip_driver_install"

# Remove trailing skip_driver_install line if present
if tail -n 1 "$CONFFILE" | grep -q "$PATTERN"; then
    sed -i '$d' "$CONFFILE"
fi

if ! grep -q "skip_driver_install" /etc/wekaio/service.conf; then
    sed -i "/\[os\]/a skip_driver_install=%s" /etc/wekaio/service.conf
    sed -i "/\[os\]/a ignore_driver_spec=%s" /etc/wekaio/service.conf
else
    sed -i "s/skip_driver_install=.*/skip_driver_install=%s/g" /etc/wekaio/service.conf
fi
sed -i "s/ignore_driver_spec=.*/ignore_driver_spec=%s/g" /etc/wekaio/service.conf || true

sed -i "s@external_mounts=.*@external_mounts=/opt/weka/external-mounts@g" /etc/wekaio/service.conf || true
sed -i "s@conditional_mounts_ids=.*@conditional_mounts_ids=kube-serviceaccount,etc-hosts,etc-resolv%s@g" /etc/wekaio/service.conf || true
%s
sed -i 's/cgroups_mode=auto/cgroups_mode=none/g' /etc/wekaio/service.conf || true
sed -i 's/override_core_pattern=true/override_core_pattern=false/g' /etc/wekaio/service.conf || true
sed -i "s/port=14100/port=%d/g" /etc/wekaio/service.conf || true
echo '{"agent": {"port": "%d"}}' > /etc/wekaio/service.json
`,
		envoyEnvExports,
		ignoreDriverFlag, ignoreDriverFlag, ignoreDriverFlag, ignoreDriverFlag,
		expandConditionMounts, skipEnvoySetup,
		cfg.AgentPort, cfg.AgentPort,
	)

	if err := cmdutil.Run(ctx, "sh", "-c", script); err != nil {
		return fmt.Errorf("agent.Configure: %w", err)
	}

	if cfg.MachineIdentifier != "" {
		logger.Info("setting machine-id", "id", cfg.MachineIdentifier)
		if err := os.MkdirAll("/opt/weka/data/agent", 0o755); err != nil {
			return err
		}
		idPath := "/opt/weka/data/agent/machine-identifier"
		if err := os.WriteFile(idPath, []byte(cfg.MachineIdentifier), 0o644); err != nil {
			return fmt.Errorf("agent.Configure machine-identifier: %w", err)
		}
	}

	return nil
}

// GetCmd returns the shell command string that starts the weka agent.
// Mirrors Python get_agent_cmd() at weka_runtime.py:3126.
func GetCmd(cfg *config.Config) string {
	return fmt.Sprintf("exec /usr/bin/weka --agent --socket-name weka_agent_ud_socket_%d", cfg.AgentPort)
}

// AwaitReady polls "weka local ps" until exit 0, with a timeout.
// Timeout is 60s normally, 1500s for global persistence mode.
// Mirrors Python await_agent() at weka_runtime.py:2075.
func AwaitReady(ctx context.Context, cfg *config.Config) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "agent.AwaitReady")
	defer logger.End()

	timeout := 60 * time.Second
	if cfg.WekaPersistenceMode == "global" {
		timeout = 1500 * time.Second
	}

	deadline := time.Now().Add(timeout)
	for {
		if err := cmdutil.Run(ctx, "weka", "local", "ps"); err == nil {
			logger.Info("weka-agent started successfully")
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("agent.AwaitReady: agent did not come up in %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
		logger.Info("waiting for weka-agent to start")
	}
}

// OverrideDependenciesFlag hard-codes the dependency success marker so the dist container can start.
// Mirrors Python override_dependencies_flag() at weka_runtime.py:2988.
func OverrideDependenciesFlag(ctx context.Context, cfg *config.Config) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "agent.OverrideDependenciesFlag")
	defer logger.End()

	logger.Info("overriding dependencies flag")

	// M2: drive both branch and dep version from ResolveVersionParams.
	// Mirrors Python weka_runtime.py:2988-3010:
	//   dep_version = version_params.get('dependencies', DEFAULT_DEPENDENCY_VERSION)
	//   if WEKA_DRIVERS_HANDLING: touch .../skip  else: mkdir .../dep_version/$(uname -r)/ && touch .../successful
	vp := drivers.ResolveVersionParams(cfg.ImageName)
	if vp.WekaDriversHandling {
		script := `
mkdir -p /opt/weka/data/dependencies
touch /opt/weka/data/dependencies/skip
`
		if err := cmdutil.Run(ctx, "sh", "-c", script); err != nil {
			return fmt.Errorf("agent.OverrideDependenciesFlag (new): %w", err)
		}
		return nil
	}

	depVersion := vp.EffectiveDependencies()
	script := fmt.Sprintf(`
mkdir -p /opt/weka/data/dependencies/%s/$(uname -r)/
touch /opt/weka/data/dependencies/%s/$(uname -r)/successful
`, depVersion, depVersion)
	if err := cmdutil.Run(ctx, "sh", "-c", script); err != nil {
		return fmt.Errorf("agent.OverrideDependenciesFlag (legacy): %w", err)
	}
	return nil
}

// EnsureDrivers polls until all required kernel drivers are loaded.
// Mirrors Python ensure_drivers() at weka_runtime.py:1208.
func EnsureDrivers(ctx context.Context, cfg *config.Config) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "agent.EnsureDrivers")
	defer logger.End()

	logger.Info("waiting for drivers", "mode", cfg.Mode)

	// Client / s3 / nfs: use "weka driver ready" command (new driver mode).
	if !isLegacyDriverMode(cfg) && isClientLikeMode(cfg.Mode) {
		// M6: read version from release spec (as Python's get_weka_version() does),
		// instead of shelling out to "weka version | grep '*' | awk ..." which requires
		// the agent to already be running.
		// Mirrors Python ensure_drivers() at weka_runtime.py:1217-1221:
		//   version = await get_weka_version()
		//   run_command(f"weka driver ready --without-agent --version {version}")
		wekaVersion, err := drivers.GetWekaVersion()
		if err != nil {
			return fmt.Errorf("EnsureDrivers: get weka version: %w", err)
		}
		if err := cmdutil.PollUntil(ctx, 1*time.Second, func() bool {
			if err := cmdutil.Run(ctx, "weka", "driver", "ready", "--without-agent", "--version", wekaVersion); err == nil {
				return true
			}
			logger.Warn("drivers not ready, waiting")
			if e := writeDriverLog("weka-drivers-loading"); e != nil {
				logger.Warn("failed to write driver status log", "err", e)
			}
			return false
		}); err != nil {
			return err
		}
		if err := writeDriverLog(""); err != nil {
			return fmt.Errorf("EnsureDrivers: clearing driver status log: %w", err)
		}
		logger.Info("all drivers loaded successfully")
		return nil
	}

	// Compute / drive: poll lsmod for each driver.
	driverModules := []string{"wekafsio", "wekafsgw", "mpin_user"}

	nodeInfo, err := osinfo.Load()
	isCOS := err == nil && nodeInfo.IsCos()
	if !isCOS {
		driverModules = append(driverModules, "igb_uio")
		if !skipUIOPCIGeneric(cfg) {
			driverModules = append(driverModules, "uio_pci_generic")
		}
	}

	for _, driver := range driverModules {
		if err := cmdutil.PollUntil(ctx, 1*time.Second, func() bool {
			if err := cmdutil.Run(ctx, "sh", "-c", fmt.Sprintf("lsmod | grep -w %s", driver)); err == nil {
				return true
			}
			logger.Info("driver not loaded, waiting", "driver", driver)
			if e := writeDriverLog(driver); e != nil {
				logger.Warn("failed to write driver status log", "err", e)
			}
			return false
		}); err != nil {
			return err
		}
	}

	if err := writeDriverLog(""); err != nil {
		return fmt.Errorf("EnsureDrivers: clearing driver status log: %w", err)
	}
	logger.Info("all drivers loaded successfully")
	return nil
}

// ---- helpers ----------------------------------------------------------------

// isLegacyDriverMode returns true when the old lsmod-based driver check should be used.
// Python: is_legacy_driver_cmd() at weka_runtime.py:3215 — checks if "weka driver --help | grep pack" succeeds.
// In Go we run the same check.
func isLegacyDriverMode(cfg *config.Config) bool {
	err := exec.Command("sh", "-c", "weka driver --help | grep pack").Run() //nolint:gosec // command args are operator-controlled, not user input
	if err == nil {
		return false // new mode: "pack" command available
	}
	return true // legacy mode
}

// isClientLikeMode returns true for modes that use weka driver ready instead of lsmod.
func isClientLikeMode(mode string) bool {
	switch mode {
	case "client", "s3", "nfs":
		return true
	}
	return false
}

// skipUIOPCIGeneric returns true when uio_pci_generic should not be loaded.
// On COS we always skip it.
func skipUIOPCIGeneric(cfg *config.Config) bool {
	// M1: mirror Python should_skip_uio_pci_generic() at weka_runtime.py:1416-1417:
	//   return version_params.get('uio_pci_generic') is False or should_skip_uio()
	// where should_skip_uio() == is_google_cos(). The version-params branch is what makes
	// all 4.3.x and DEFAULT_PARAMS images skip uio_pci_generic even on non-COS nodes.
	if drivers.ResolveVersionParams(cfg.ImageName).ShouldSkipUioPciGeneric() {
		return true
	}
	nodeInfo, err := osinfo.Load()
	if err == nil && nodeInfo.IsCos() {
		return true
	}
	return false
}

// writeDriverLog writes the driver name to /tmp/weka-drivers.log atomically.
func writeDriverLog(content string) error {
	const tmp = "/tmp/weka-drivers.log_tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, "/tmp/weka-drivers.log")
}
