// Package cos handles Google Container-Optimized OS specific operations.
package cos

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/osinfo"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// ConfigureHugepages checks and optionally modifies the hugepage configuration on Google COS nodes.
// On non-COS nodes this is a no-op. If hugepages need to change and
// host.AllowHugepageConfig is true, the node reboots via sysrq-trigger.
// If hugepages need to change but the flag is false, an error is returned (matching Python behavior).
func ConfigureHugepages(ctx context.Context, runner process.CommandRunner, host config.Host) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "ConfigureHugepages")
	defer logger.End()

	nodeInfo, err := osinfo.Load()
	if err != nil || !nodeInfo.IsCos() {
		logger.Info("Skipping hugepages configuration (non-COS node)")
		return nil
	}

	// Check if hugepages already configured
	if count, countErr := currentHugepageCount(); countErr == nil && count > 0 {
		logger.Info("Node already has hugepages configured, skipping", "count", count)
		return nil
	}

	logger.Info("Checking if hugepages need to be set")

	cmdline, err := os.ReadFile("/hostside/proc/cmdline")
	if err != nil {
		return fmt.Errorf("reading /hostside/proc/cmdline: %w", err)
	}

	line := strings.TrimSpace(string(cmdline))
	logger.Info("Kernel cmdline", "cmdline", line)

	var sedCmds []GrubSed

	// Handle hugepagesize mismatch (always, regardless of flag — matches Python)
	if strings.Contains(line, "hugepagesz=") {
		wantSize := strings.ToLower(host.GlobalHugepageSize)
		if strings.Contains(strings.ToLower(line), "hugepagesz=1g") && wantSize == "2m" {
			sedCmds = append(sedCmds, GrubSed{"hugepagesz=1g", "hugepagesz=2m"})
		} else if strings.Contains(strings.ToLower(line), "hugepagesz=2m") && wantSize == "1g" {
			sedCmds = append(sedCmds, GrubSed{"hugepagesz=2m", "hugepagesz=1g"})
		}
	}

	// Handle hugepages count
	wantCount := host.GlobalHugepageCount
	wantStr := fmt.Sprintf("hugepages=%d", wantCount)
	if !strings.Contains(line, "hugepages=") {
		// hugepages= not set at all — always add it (regardless of flag, matching Python)
		sedCmds = append(sedCmds, GrubSed{"cros_efi", fmt.Sprintf("cros_efi %s", wantStr)})
	} else if !strings.Contains(line, wantStr) {
		if host.AllowHugepageConfig {
			// hugepages set but wrong value, and we're allowed to change it
			sedCmds = append(sedCmds, GrubSed{`hugepages=[0-9]+`, wantStr})
		} else {
			logger.Info("Node hugepages configuration is managed externally, skipping")
		}
	}

	if len(sedCmds) == 0 {
		logger.Info("Hugepages already configured", "count", wantCount)
		return nil
	}

	logger.Info("Must modify kernel HUGEPAGES parameters")
	if !host.AllowHugepageConfig {
		return fmt.Errorf("hugepage configuration must change but WEKA_COS_ALLOW_HUGEPAGE_CONFIG is not set")
	}
	logger.Info("Node hugepage configuration has changed, NODE WILL REBOOT NOW!")

	if err := RewriteGrubAndReboot(ctx, runner, sedCmds, "/proc/sysrq-trigger"); err != nil {
		return err
	}
	// Block until the reboot actually happens
	<-ctx.Done()
	return ctx.Err()
}

func currentHugepageCount() (int, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }() //nolint:errcheck // close error on read-only file is not actionable
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "HugePages_Total:") {
			var count int
			_, _ = fmt.Sscanf(strings.TrimPrefix(line, "HugePages_Total:"), "%d", &count) //nolint:errcheck // partial scan is acceptable
			return count, nil
		}
	}
	return 0, fmt.Errorf("HugePages_Total not found in /proc/meminfo")
}
