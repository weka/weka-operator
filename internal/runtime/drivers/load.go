package drivers

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/osinfo"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
)

// SetupOverlayfsForLibModules mounts a tmpfs-backed overlayfs over /lib/modules so
// that the kernel driver installer can write into what is typically a read-only host mount.
func SetupOverlayfsForLibModules(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "drivers.SetupOverlayfsForLibModules")
	defer logger.End()

	realPathBytes, err := cmdutil.Output(ctx, "readlink", "-f", "/lib/modules")
	if err != nil {
		return fmt.Errorf("SetupOverlayfsForLibModules: readlink: %w", err)
	}
	realPath := strings.TrimSpace(string(realPathBytes))

	const ovlBase = "/tmp/ovl-libmodules"
	upperDir := ovlBase + "/upper"
	workDir := ovlBase + "/work"
	ovlMnt := ovlBase + "/mnt"

	if err := os.MkdirAll(ovlBase, 0o755); err != nil {
		return fmt.Errorf("SetupOverlayfsForLibModules: mkdir %s: %w", ovlBase, err)
	}

	// L4: Skip the tmpfs mount if ovlBase is already a mountpoint (idempotency guard).
	// Mirrors Python weka_runtime.py:1565-1570:
	//   if (await run_command(f"mountpoint -q {ovl_root}"))[2] != 0: mount tmpfs ...
	if err := cmdutil.Run(ctx, "mountpoint", "-q", ovlBase); err != nil {
		if err := cmdutil.Run(ctx, "mount", "-t", "tmpfs", "-o", "size=512m", "tmpfs", ovlBase); err != nil {
			return fmt.Errorf("SetupOverlayfsForLibModules: mount tmpfs: %w", err)
		}
	}

	for _, dir := range []string{upperDir, workDir, ovlMnt} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("SetupOverlayfsForLibModules: mkdir %s: %w", dir, err)
		}
	}

	overlayOpts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", realPath, upperDir, workDir)
	if err := cmdutil.Run(ctx, "mount", "-t", "overlay", "overlay", "-o", overlayOpts, ovlMnt); err != nil {
		return fmt.Errorf("SetupOverlayfsForLibModules: mount overlay: %w", err)
	}

	if err := cmdutil.Run(ctx, "mount", "--bind", ovlMnt, realPath); err != nil {
		return fmt.Errorf("SetupOverlayfsForLibModules: bind mount to %s: %w", realPath, err)
	}

	if realPath != "/lib/modules" {
		if err := cmdutil.Run(ctx, "mount", "--bind", realPath, "/lib/modules"); err != nil {
			return fmt.Errorf("SetupOverlayfsForLibModules: bind mount to /lib/modules: %w", err)
		}
	}

	logger.Info("overlayfs for /lib/modules set up", "realPath", realPath)
	return nil
}

// DisableDriverSigning handles COS-specific kernel module signature enforcement.
// allowDisableSign should be cfg.COSAllowDisableDriverSign. On non-COS nodes it is a no-op.
func DisableDriverSigning(ctx context.Context, allowDisableSign bool) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "drivers.DisableDriverSigning")
	defer logger.End()

	nodeInfo, err := osinfo.Load()
	if err != nil || !nodeInfo.IsCos() {
		return nil
	}

	logger.Info("checking kernel driver signing enforcement on COS")
	return cosDisableDriverSigning(ctx, allowDisableSign)
}

func cosDisableDriverSigning(ctx context.Context, allowDisableSign bool) error {
	cmdlineData, err := os.ReadFile("/hostside/proc/cmdline")
	if err != nil {
		return fmt.Errorf("cosDisableDriverSigning: read cmdline: %w", err)
	}
	line := string(cmdlineData)

	type sedCmd struct{ from, to string }
	var cmds []sedCmd

	if strings.Contains(line, "module.sig_enforce") {
		if strings.Contains(line, "module.sig_enforce=1") {
			cmds = append(cmds, sedCmd{"module.sig_enforce=1", "module.sig_enforce=0"})
		}
	} else {
		cmds = append(cmds, sedCmd{"cros_efi", "cros_efi module.sig_enforce=0"})
	}
	if strings.Contains(line, "loadpin.enabled") {
		if strings.Contains(line, "loadpin.enabled=1") {
			cmds = append(cmds, sedCmd{"loadpin.enabled=1", "loadpin.enabled=0"})
		}
	} else {
		cmds = append(cmds, sedCmd{"cros_efi", "cros_efi loadpin.enabled=0"})
	}
	if strings.Contains(line, "loadpin.enforce") {
		if strings.Contains(line, "loadpin.enforce=1") {
			cmds = append(cmds, sedCmd{"loadpin.enforce=1", "loadpin.enforce=0"})
		}
	} else {
		cmds = append(cmds, sedCmd{"cros_efi", "cros_efi loadpin.enforce=0"})
	}

	if len(cmds) == 0 {
		return nil
	}

	if !allowDisableSign {
		return fmt.Errorf("node driver signing must be disabled but WEKA_COS_ALLOW_DISABLE_DRIVER_SIGNING is not set")
	}

	const espPartition = "/dev/disk/by-partlabel/EFI-SYSTEM"
	const mountPath = "/tmp/esp"
	const grubCfg = "efi/boot/grub.cfg"

	if err := os.MkdirAll(mountPath, 0o755); err != nil {
		return err
	}
	if err := cmdutil.Run(ctx, "mount", espPartition, mountPath); err != nil {
		return fmt.Errorf("cosDisableDriverSigning: mount ESP: %w", err)
	}
	defer func() { _ = cmdutil.Run(ctx, "umount", mountPath) }() //nolint:errcheck // best-effort cleanup on defer

	for _, sc := range cmds {
		script := fmt.Sprintf("cd %s && sed -i 's/%s/%s/g' %s", mountPath, sc.from, sc.to, grubCfg)
		if err := cmdutil.Run(ctx, "sh", "-c", script); err != nil {
			return fmt.Errorf("cosDisableDriverSigning: sed: %w", err)
		}
	}

	// Reboot via sysrq-trigger.
	_ = os.WriteFile("/hostside/proc/sysrq-trigger", []byte("b"), 0o200) //nolint:errcheck // reboot trigger: process ends immediately after
	return nil
}

// LoadModules runs the post-load steps common to both legacy and new driver modes:
// vfio-pci, arp_tables, and optionally uio_pci_generic.
func LoadModules(ctx context.Context, skipUIOPCIGeneric bool) {
	_, logger := instrumentation.CreateLogSpan(ctx, "drivers.LoadModules")
	defer logger.End()

	nodeInfo, err := osinfo.Load()
	isCOS := err == nil && nodeInfo != nil && nodeInfo.IsCos()

	loadVfioPCI := func() {
		if isCOS {
			_ = cmdutil.Run(ctx, "modprobe", "vfio-pci") //nolint:errcheck // best-effort: module may already be loaded
			return
		}
		entries, err := os.ReadDir("/sys/kernel/iommu_groups/")
		if err == nil && len(entries) > 0 {
			_ = cmdutil.Run(ctx, "modprobe", "vfio-pci") //nolint:errcheck // best-effort: module may already be loaded
		}
	}
	loadVfioPCI()

	if err := cmdutil.Run(ctx, "modprobe", "arp_tables"); err != nil {
		logger.Warn("failed to load arp_tables (non-fatal)", "err", err)
	}

	if !skipUIOPCIGeneric {
		if err := cmdutil.Run(ctx, "modprobe", "uio_pci_generic"); err != nil {
			logger.Warn("failed to load uio_pci_generic (non-fatal)", "err", err)
		}
	}
}
