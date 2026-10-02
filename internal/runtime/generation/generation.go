// Package generation manages the weka_runtime generation file used for takeover detection.
// Mirrors write_generation, obtain_lock, is_wrong_generation, get_boot_id at weka_runtime.py:2776–4344.
package generation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/paths"
)

const persistBindsSubdir = "opt-weka"
const generationFile = "runtime-generation"
const persistencyMarkerFile = "persistency-configured"

// Publish waits for persistency to be configured (if needed), then writes a fresh generation
// marker and returns it.
// Mirrors Python write_generation() at weka_runtime.py:3290.
func Publish(ctx context.Context, p paths.Roots) (string, error) { //nolint:gocritic // value semantics preferred over pointer churn for this cold-path config struct
	_, logger := instrumentation.CreateLogSpan(ctx, "generation.Publish")
	defer logger.End()

	persistBindsDir := p.HostBinds + "/" + persistBindsSubdir
	persistencyMarker := p.K8sRuntime + "/" + persistencyMarkerFile
	generationPath := p.K8sRuntime + "/" + generationFile

	// Wait while the persist-binds dir exists but persistency is not yet configured.
	if err := clock.Poll(ctx, clock.System, 1*time.Second, func() (bool, error) {
		_, errBinds := os.Stat(persistBindsDir)
		_, errMarker := os.Stat(persistencyMarker)
		if os.IsNotExist(errBinds) || errMarker == nil {
			return true, nil
		}
		logger.Info("Waiting for persistency to be configured")
		return false, nil
	}); err != nil {
		return "", fmt.Errorf("generation.Publish: waiting for persistency: %w", err)
	}

	marker := fmt.Sprintf("%f", float64(time.Now().UnixNano())/1e9)
	logger.Info("Writing generation", "generation", marker)
	if err := os.MkdirAll(p.K8sRuntime, 0o755); err != nil {
		return "", fmt.Errorf("generation.Publish mkdir: %w", err)
	}
	if err := os.WriteFile(generationPath, []byte(marker), 0o644); err != nil {
		return "", fmt.Errorf("generation.Publish: %w", err)
	}
	return marker, nil
}

// AcquireLock binds an abstract-namespace UNIX socket to provide an exclusive runtime lock,
// with a single bind attempt (no retries): the second bind on an already-held name fails.
// Mirrors Python obtain_lock() at weka_runtime.py:3312.
func AcquireLock(name string) (io.Closer, error) {
	return net.ListenPacket("unixgram", "\x00weka_runtime_"+name)
}

// IsTakenOver reports whether the on-disk generation marker differs from marker, meaning
// another instance has taken over. A missing or whitespace-only on-disk marker is not a
// takeover.
// Mirrors Python is_wrong_generation() at weka_runtime.py:4325.
func IsTakenOver(p paths.Roots, marker string) (bool, error) { //nolint:gocritic // value semantics preferred over pointer churn for this cold-path config struct
	generationPath := p.K8sRuntime + "/" + generationFile

	content, err := os.ReadFile(generationPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("generation.IsTakenOver: %w", err)
	}
	onDisk := strings.TrimSpace(string(content))
	if onDisk == "" {
		return false, nil
	}
	return onDisk != marker, nil
}

// ReadBootID reads /proc/sys/kernel/random/boot_id.
// Returns empty string on error (non-fatal: callers treat empty as unknown).
func ReadBootID() string {
	content, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		fmt.Fprintf(os.Stderr, "generation.ReadBootID: %v\n", err)
		return ""
	}
	return strings.TrimSpace(string(content))
}
