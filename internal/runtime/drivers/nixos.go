package drivers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/osinfo"
	"github.com/weka/weka-operator/internal/runtime/process"
)

const kernelReleasePath = "/proc/sys/kernel/osrelease"

// PrepareNixosHostKernel wires kbuild and modprobe, inside the NixOS builder image, to the
// host's kernel headers and in-tree modules exposed under /host/: /host/current-system is the
// host's /run/current-system (headers at sw/lib/modules/<kver>/build, modules at
// kernel-modules/lib/modules/<kver>) and /host/nix/store is its store. The host store is
// unioned under the image's own store so both symlink farms resolve at their canonical
// /nix/store paths, then /lib/modules/<kver> is populated from them for kbuild and modprobe.
func PrepareNixosHostKernel(ctx context.Context, runner process.CommandRunner) error {
	kverRaw, err := os.ReadFile(kernelReleasePath)
	if err != nil {
		return fmt.Errorf("PrepareNixosHostKernel: read %s: %w", kernelReleasePath, err)
	}
	procVersion, err := osinfo.ReadProcVersion()
	if err != nil {
		return fmt.Errorf("PrepareNixosHostKernel: %w", err)
	}
	nodeInfo, err := osinfo.Load()
	if err != nil {
		return fmt.Errorf("PrepareNixosHostKernel: load osinfo: %w", err)
	}
	return prepareNixosHostKernel(ctx, runner, strings.TrimSpace(string(kverRaw)), procVersion, nodeInfo.Os)
}

func prepareNixosHostKernel(ctx context.Context, runner process.CommandRunner, kver, procVersion, osDistro string) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "drivers.PrepareNixosHostKernel", "kernel", kver)
	defer logger.End()

	// The NixOS builder image's PATH lacks /usr/bin, where the operator stages the weka CLI as
	// /usr/bin/weka via a subPath mount; child processes inherit this PATH and must still find it.
	if err := ensureOnPath("/usr/local/bin", "/usr/bin"); err != nil {
		return err
	}

	headers := fmt.Sprintf("/host/current-system/sw/lib/modules/%s/build", kver)
	modules := fmt.Sprintf("/host/current-system/kernel-modules/lib/modules/%s", kver)

	// A driver built with a different gcc major than the one that built the running kernel
	// will not load, and NixOS builder images are pinned to one gcc major: fail here rather
	// than at module load time.
	gccCmd := process.Shell("gcc -dumpversion")
	gccCmd.Log = process.LogOutput
	res, err := runner.Run(ctx, gccCmd)
	if err != nil {
		return fmt.Errorf("failed to determine builder image gcc version: %s: %w", stderrOf(err), err)
	}
	imgGcc, _, _ := strings.Cut(strings.TrimSpace(string(res.Stdout)), ".")
	kernGcc, err := osinfo.ParseGccMajor(procVersion)
	if err != nil {
		return err
	}
	if imgGcc != kernGcc {
		return fmt.Errorf("builder image gcc %s does not match kernel gcc %s (%s); use the builder image for %s",
			imgGcc, kernGcc, procVersion, osDistro)
	}

	steps := []struct{ desc, script string }{
		{
			"checking NixOS host mounts are present",
			`for m in /host/nix/store /host/current-system; do ` +
				`[ -e "$m" ] || { echo "missing host mount: $m" >&2; exit 1; }; done`,
		},
		{
			// Image paths win: a plain bind of the host store would hide the image's own store
			// paths that the builder's helper binaries need.
			"unioning host nix store under the image store",
			`if ! mountpoint -q /tmp/nix-store-union; then ` +
				`mkdir -p /tmp/nix-store-union && ` +
				`mount -t overlay overlay -o lowerdir=/nix/store:/host/nix/store /tmp/nix-store-union && ` +
				`mount --bind /tmp/nix-store-union /nix/store; fi`,
		},
		// Both host trees are symlink farms into /nix/store, so they resolve only after the union.
		{
			fmt.Sprintf("validating host kernel headers for running kernel %s", kver),
			fmt.Sprintf(`[ -f "%s/Makefile" ] || { echo "no kernel headers for running kernel %s at %s: `+
				`host needs environment.systemPackages = [ kernel.dev ], or a reboot after a kernel change" >&2; exit 1; }`,
				headers, kver, headers),
		},
		{
			fmt.Sprintf("validating host module tree for running kernel %s", kver),
			fmt.Sprintf(`[ -d %q ] || { echo "no module tree for running kernel %s at %s" >&2; exit 1; }`, modules, kver, modules),
		},
		{
			// kbuild must be invoked at the canonical store path the headers were generated at,
			// not the mount path.
			"linking kernel headers into /lib/modules",
			fmt.Sprintf(`mkdir -p /lib/modules/%s && ln -sfn "$(readlink -f %q)" /lib/modules/%s/build`, kver, headers, kver),
		},
		{
			// In-tree modules (uio etc.) for modprobe dependency resolution, resolved to their
			// store targets so they work without /run/current-system mounted at its host path.
			"linking in-tree kernel modules into /lib/modules",
			fmt.Sprintf(`for e in "%s"/*; do b=$(basename "$e"); `+
				`[ -e "/lib/modules/%s/$b" ] || ln -s "$(readlink -f "$e")" "/lib/modules/%s/$b"; done`, modules, kver, kver),
		},
		{"running depmod", fmt.Sprintf(`depmod -a %q`, kver)},
	}
	for _, s := range steps {
		logger.Info("NixOS host kernel prep step", "step", s.desc)
		if _, err := runner.Run(ctx, process.Shell(s.script)); err != nil {
			return fmt.Errorf("failed to prepare NixOS host kernel (%s): %s: %w", s.desc, stderrOf(err), err)
		}
	}

	logger.Info("NixOS host kernel prepared")
	return nil
}

func ensureOnPath(dirs ...string) error {
	for _, d := range dirs {
		cur := os.Getenv("PATH")
		if !containsPathEntry(cur, d) {
			if err := os.Setenv("PATH", d+":"+cur); err != nil {
				return fmt.Errorf("set PATH: %w", err)
			}
		}
	}
	return nil
}

func containsPathEntry(path, entry string) bool {
	for _, p := range strings.Split(path, ":") {
		if p == entry {
			return true
		}
	}
	return false
}

func stderrOf(err error) string {
	var execErr *process.ExecError
	if errors.As(err, &execErr) {
		return string(execErr.Result.Stderr)
	}
	return ""
}
