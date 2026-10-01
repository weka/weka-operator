package cos

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/weka/weka-operator/internal/runtime/process"
)

// fakeRunner records issued commands; mount/umount are no-ops so tests can
// operate on a real grub.cfg fixture under espMountPath without a real mount.
type fakeRunner struct {
	commands []process.Command
}

func (f *fakeRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.commands = append(f.commands, c)
	return process.Result{}, nil
}

// TestRewriteGrubAndReboot_AppliesSedsAndTriggersReboot verifies each GrubSed is applied to
// grub.cfg via regexp replacement and that "b" is written to sysrqPath.
func TestRewriteGrubAndReboot_AppliesSedsAndTriggersReboot(t *testing.T) {
	dir := t.TempDir()
	origMountPath := espMountPath
	espMountPath = dir
	defer func() { espMountPath = origMountPath }()

	if err := os.MkdirAll(filepath.Join(dir, "efi/boot"), 0o755); err != nil {
		t.Fatalf("setup grub dir: %v", err)
	}
	grubPath := filepath.Join(dir, grubCfgPath)
	if err := os.WriteFile(grubPath, []byte("linux cros_efi hugepages=0\n"), 0o644); err != nil {
		t.Fatalf("setup grub.cfg: %v", err)
	}

	sysrqPath := filepath.Join(dir, "sysrq-trigger")
	if err := os.WriteFile(sysrqPath, []byte{}, 0o644); err != nil {
		t.Fatalf("setup sysrq file: %v", err)
	}

	runner := &fakeRunner{}
	seds := []GrubSed{{From: `hugepages=[0-9]+`, To: "hugepages=1024"}}

	if err := RewriteGrubAndReboot(context.Background(), runner, seds, sysrqPath); err != nil {
		t.Fatalf("RewriteGrubAndReboot() error = %v", err)
	}

	got, err := os.ReadFile(grubPath)
	if err != nil {
		t.Fatalf("reading grub.cfg: %v", err)
	}
	if want := "linux cros_efi hugepages=1024\n"; string(got) != want {
		t.Errorf("grub.cfg = %q, want %q", got, want)
	}

	trigger, err := os.ReadFile(sysrqPath)
	if err != nil {
		t.Fatalf("reading sysrq file: %v", err)
	}
	if string(trigger) != "b" {
		t.Errorf("sysrq trigger = %q, want %q", trigger, "b")
	}
}

// TestRewriteGrubAndReboot_InvalidRegexpReturnsError verifies a bad sed pattern surfaces an error
// instead of silently continuing.
func TestRewriteGrubAndReboot_InvalidRegexpReturnsError(t *testing.T) {
	dir := t.TempDir()
	origMountPath := espMountPath
	espMountPath = dir
	defer func() { espMountPath = origMountPath }()

	if err := os.MkdirAll(filepath.Join(dir, "efi/boot"), 0o755); err != nil {
		t.Fatalf("setup grub dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, grubCfgPath), []byte("linux cros_efi\n"), 0o644); err != nil {
		t.Fatalf("setup grub.cfg: %v", err)
	}

	runner := &fakeRunner{}
	seds := []GrubSed{{From: "(", To: "x"}}

	if err := RewriteGrubAndReboot(context.Background(), runner, seds, filepath.Join(dir, "sysrq-trigger")); err == nil {
		t.Fatal("expected error for invalid regexp, got nil")
	}
}
