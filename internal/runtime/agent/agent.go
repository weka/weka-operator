// Package agent configures and manages the weka-agent process.
// Mirrors configure_agent, get_agent_cmd, await_agent, ensure_drivers, override_dependencies_flag
// at weka_runtime.py:1208–3128.
package agent

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/osinfo"
	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/drivers"
	"github.com/weka/weka-operator/internal/runtime/paths"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// agentPortScript rewrites the port key under the [agent] section only, then verifies
// the rewrite landed. sed exits 0 on no-match, and the runner does not run scripts
// under "set -e" (unlike Python's run_command), so the verification failure is made
// explicit here rather than relying on shell abort semantics.
const agentPortScript = `sed -i "/^\[agent\]/,/^\[/ s/^port=.*/port=%d/" /etc/wekaio/service.conf
sed -n "/^\[agent\]/,/^\[/p" /etc/wekaio/service.conf | grep -qx "port=%d" || { echo "agent port rewrite verification failed" >&2; exit 1; }
`

// ConfigureInput is the narrow set of config sections Configure needs.
type ConfigureInput struct {
	Mode        string
	Identity    config.Identity
	Agent       config.Agent
	Ports       config.Ports
	Persistence config.Persistence
	Paths       paths.Roots
}

// Configure patches /etc/wekaio/service.conf and writes /etc/wekaio/service.json.
// handleDrivers=false means agent should NOT handle drivers (compute/drive/client pass false).
// Mirrors Python configure_agent() at weka_runtime.py:2924.
func Configure(ctx context.Context, runner process.CommandRunner, in ConfigureInput, handleDrivers bool) error { //nolint:gocritic // value semantics preferred over pointer churn for this cold-path config struct
	_, logger := instrumentation.CreateLogSpan(ctx, "agent.Configure")
	defer logger.End()

	ignoreDriverFlag := "true"
	if handleDrivers {
		ignoreDriverFlag = "false"
	}

	expandConditionMounts := ""
	if in.Mode == "s3" || in.Mode == "envoy" {
		expandConditionMounts = ",envoy-data"
	}

	skipEnvoySetup := ""
	if in.Mode == "s3" {
		skipEnvoySetup = "sed -i 's/skip_envoy_setup=.*/skip_envoy_setup=true/g' /etc/wekaio/service.conf || true"
	}

	// weka images do not always ship a [mounts] section, so create it before setting the key.
	// Mirrors Python configure_agent() no_reserve_space_cmd at weka_runtime.py:3327.
	noReserveSpaceCmd := ""
	if in.Agent.NoReserveSpace {
		noReserveSpaceCmd = `
grep -q "^\[mounts\]" /etc/wekaio/service.conf || printf '\n[mounts]\n' >> /etc/wekaio/service.conf
if grep -qE "^[[:space:]]*allocate_reserved_space[[:space:]]*=" /etc/wekaio/service.conf; then
    sed -i -E "s/^[[:space:]]*allocate_reserved_space[[:space:]]*=.*/allocate_reserved_space=false/g" /etc/wekaio/service.conf
else
    sed -i "/^\[mounts\]/a allocate_reserved_space=false" /etc/wekaio/service.conf
fi
`
	}

	// M5: Envoy agent env vars.
	// Mirrors Python configure_agent() at weka_runtime.py:2934-2936:
	//   if MODE == "envoy":
	//       env_vars['RESTART_EPOCH_WANTED'] = str(int(os.environ.get("envoy_restart_epoch", time.time())))
	//       env_vars['BASE_ID'] = PORT
	envoyEnvExports := ""
	if in.Mode == "envoy" {
		restartEpoch := in.Agent.EnvoyEpoch
		if restartEpoch == "" {
			restartEpoch = fmt.Sprintf("%d", time.Now().Unix())
		}
		envoyEnvExports = fmt.Sprintf("export RESTART_EPOCH_WANTED=%s\nexport BASE_ID=%d\n",
			restartEpoch, in.Ports.Weka)
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
%s
sed -i 's/cgroups_mode=auto/cgroups_mode=none/g' /etc/wekaio/service.conf || true
sed -i 's/override_core_pattern=true/override_core_pattern=false/g' /etc/wekaio/service.conf || true
%s
echo '{"agent": {"port": %d}}' > /etc/wekaio/service.json
`,
		envoyEnvExports,
		ignoreDriverFlag, ignoreDriverFlag, ignoreDriverFlag, ignoreDriverFlag,
		expandConditionMounts, skipEnvoySetup, noReserveSpaceCmd,
		fmt.Sprintf(agentPortScript, in.Ports.Agent, in.Ports.Agent), in.Ports.Agent,
	)

	if _, err := runner.Run(ctx, process.Command{Path: "sh", Args: []string{"-c", script}}); err != nil {
		return fmt.Errorf("agent.Configure: %w", err)
	}

	if in.Identity.MachineIdentifier != "" {
		logger.Info("setting machine-id", "id", in.Identity.MachineIdentifier)
		agentDataDir := in.Paths.OptWeka + "/data/agent"
		if err := os.MkdirAll(agentDataDir, 0o755); err != nil {
			return err
		}
		idPath := agentDataDir + "/machine-identifier"
		if err := os.WriteFile(idPath, []byte(in.Identity.MachineIdentifier), 0o644); err != nil {
			return fmt.Errorf("agent.Configure machine-identifier: %w", err)
		}
	}

	return nil
}

// Command returns the weka-agent daemon command. It does not start it.
// Mirrors Python get_agent_cmd() at weka_runtime.py:3126.
func Command(agentPort int) process.Command {
	return process.Command{
		Path: "/usr/bin/weka",
		Args: []string{"--agent", "--socket-name", fmt.Sprintf("weka_agent_ud_socket_%d", agentPort)},
	}
}

// AwaitReady polls "weka local ps" until exit 0, or until timeout elapses.
// Mirrors Python await_agent() at weka_runtime.py:2075.
func AwaitReady(ctx context.Context, runner process.CommandRunner, c clock.Clock, timeout time.Duration) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "agent.AwaitReady")
	defer logger.End()

	start := c.Now()
	deadline := start.Add(timeout)
	for {
		// Mirror Python await_agent (weka_runtime.py:2137): poll `weka local ps`.
		if _, err := runner.Run(ctx, process.Command{Path: "weka", Args: []string{"local", "ps"}}); err == nil {
			logger.Info("Weka-agent started successfully")
			return nil
		}
		if c.Now().After(deadline) {
			return fmt.Errorf("agent.AwaitReady: agent did not come up in %s", timeout)
		}
		if err := clock.Sleep(ctx, c, 300*time.Millisecond); err != nil {
			return err
		}
		logger.Info("Waiting for weka-agent to start", "elapsed_s", int(c.Now().Sub(start).Seconds()))
	}
}

// OverrideDependenciesFlag hard-codes the dependency success marker so the dist container can start.
// Mirrors Python override_dependencies_flag().
func OverrideDependenciesFlag(ctx context.Context, runner process.CommandRunner, imageName string) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "agent.OverrideDependenciesFlag")
	defer logger.End()

	logger.Info("overriding dependencies flag")

	// M2: drive both branch and dep version from ResolveVersionParams.
	// Mirrors Python override_dependencies_flag():
	//   dep_version = version_params.get('dependencies', DEFAULT_DEPENDENCY_VERSION)
	//   if WEKA_DRIVERS_HANDLING: touch .../skip  else: mkdir .../dep_version/$(uname -r)/ && touch .../successful
	vp := drivers.ResolveVersionParams(imageName)
	if vp.WekaDriversHandling {
		script := `
mkdir -p /opt/weka/data/dependencies
touch /opt/weka/data/dependencies/skip
`
		if _, err := runner.Run(ctx, process.Command{Path: "sh", Args: []string{"-c", script}}); err != nil {
			return fmt.Errorf("agent.OverrideDependenciesFlag (new): %w", err)
		}
		return nil
	}

	depVersion := vp.EffectiveDependencies()
	script := fmt.Sprintf(`
mkdir -p /opt/weka/data/dependencies/%s/$(uname -r)/
touch /opt/weka/data/dependencies/%s/$(uname -r)/successful
`, depVersion, depVersion)
	if _, err := runner.Run(ctx, process.Command{Path: "sh", Args: []string{"-c", script}}); err != nil {
		return fmt.Errorf("agent.OverrideDependenciesFlag (legacy): %w", err)
	}
	return nil
}

// EnsureDrivers polls until all required kernel drivers are loaded.
// Mirrors Python ensure_drivers() at weka_runtime.py:1208.
func EnsureDrivers(ctx context.Context, runner process.CommandRunner, c clock.Clock, mode, imageName, optWekaRoot string) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "agent.EnsureDrivers")
	defer logger.End()

	logger.Info("waiting for drivers", "mode", mode)

	// Client / s3 / nfs: use "weka driver ready" command (new driver mode).
	if !isLegacyDriverMode(ctx, runner) && isClientLikeMode(mode) {
		// M6: read version from release spec (as Python's get_weka_version() does),
		// instead of shelling out to "weka version | grep '*' | awk ..." which requires
		// the agent to already be running.
		// Mirrors Python ensure_drivers() at weka_runtime.py:1217-1221:
		//   version = await get_weka_version()
		//   run_command(f"weka driver ready --without-agent --version {version}")
		wekaVersion, err := drivers.GetWekaVersion(optWekaRoot)
		if err != nil {
			return fmt.Errorf("EnsureDrivers: get weka version: %w", err)
		}
		if err := clock.Poll(ctx, c, 1*time.Second, func() (bool, error) {
			if _, err := runner.Run(ctx, process.Command{Path: "weka", Args: []string{"driver", "ready", "--without-agent", "--version", wekaVersion}}); err == nil {
				return true, nil
			}
			logger.Warn("drivers not ready, waiting")
			if e := writeDriverLog("weka-drivers-loading"); e != nil {
				logger.Warn("failed to write driver status log", "err", e)
			}
			return false, nil
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
		if !skipUIOPCIGeneric(imageName) {
			driverModules = append(driverModules, "uio_pci_generic")
		}
	}

	for _, driver := range driverModules {
		driver := driver
		if err := clock.Poll(ctx, c, 1*time.Second, func() (bool, error) {
			if _, err := runner.Run(ctx, process.Command{Path: "sh", Args: []string{"-c", fmt.Sprintf("lsmod | grep -w %s", driver)}}); err == nil {
				return true, nil
			}
			logger.Info("driver not loaded, waiting", "driver", driver)
			if e := writeDriverLog(driver); e != nil {
				logger.Warn("failed to write driver status log", "err", e)
			}
			return false, nil
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
func isLegacyDriverMode(ctx context.Context, runner process.CommandRunner) bool {
	_, err := runner.Run(ctx, process.Command{Path: "sh", Args: []string{"-c", "weka driver --help | grep pack"}})
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
func skipUIOPCIGeneric(imageName string) bool {
	// M1: mirror Python should_skip_uio_pci_generic() at weka_runtime.py:1416-1417:
	//   return version_params.get('uio_pci_generic') is False or should_skip_uio()
	// where should_skip_uio() == is_google_cos(). The version-params branch is what makes
	// all 4.3.x and DEFAULT_PARAMS images skip uio_pci_generic even on non-COS nodes.
	if drivers.ResolveVersionParams(imageName).ShouldSkipUioPciGeneric() {
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
