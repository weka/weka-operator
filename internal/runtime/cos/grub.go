package cos

import (
	"context"
	"fmt"
	"os"
	"regexp"

	"github.com/weka/weka-operator/internal/runtime/process"
)

// espMountPath is where the EFI System Partition is mounted to edit grub.cfg.
// A package var so tests can point it at a temp directory.
var espMountPath = "/tmp/esp"

const (
	espPartition = "/dev/disk/by-partlabel/EFI-SYSTEM"
	grubCfgPath  = "efi/boot/grub.cfg"
)

// GrubSed is a regexp-based find/replace applied to grub.cfg.
type GrubSed struct{ From, To string }

// RewriteGrubAndReboot mounts the EFI System Partition, applies seds to grub.cfg via regexp
// replacement, unmounts, and triggers a reboot by writing "b" to sysrqPath.
func RewriteGrubAndReboot(ctx context.Context, runner process.CommandRunner, seds []GrubSed, sysrqPath string) error {
	if _, err := runner.Run(ctx, process.Command{Path: "mkdir", Args: []string{"-p", espMountPath}}); err != nil {
		return fmt.Errorf("mkdir esp: %w", err)
	}
	if _, err := runner.Run(ctx, process.Command{Path: "mount", Args: []string{espPartition, espMountPath}}); err != nil {
		return fmt.Errorf("mount esp: %w", err)
	}

	grubPath := espMountPath + "/" + grubCfgPath
	grubData, err := os.ReadFile(grubPath)
	if err != nil {
		_, _ = runner.Run(ctx, process.Command{Path: "umount", Args: []string{espMountPath}}) //nolint:errcheck // best-effort cleanup
		return fmt.Errorf("reading grub.cfg: %w", err)
	}

	content := string(grubData)
	for _, s := range seds {
		re, reErr := regexp.Compile(s.From)
		if reErr != nil {
			_, _ = runner.Run(ctx, process.Command{Path: "umount", Args: []string{espMountPath}}) //nolint:errcheck // best-effort cleanup
			return fmt.Errorf("compiling regexp %q: %w", s.From, reErr)
		}
		content = re.ReplaceAllString(content, s.To)
	}

	if err := os.WriteFile(grubPath, []byte(content), 0o644); err != nil {
		_, _ = runner.Run(ctx, process.Command{Path: "umount", Args: []string{espMountPath}}) //nolint:errcheck // best-effort cleanup
		return fmt.Errorf("writing grub.cfg: %w", err)
	}

	_, _ = runner.Run(ctx, process.Command{Path: "umount", Args: []string{espMountPath}}) //nolint:errcheck // best-effort cleanup

	if err := os.WriteFile(sysrqPath, []byte("b"), 0o200); err != nil {
		return fmt.Errorf("triggering reboot: %w", err)
	}
	return nil
}
