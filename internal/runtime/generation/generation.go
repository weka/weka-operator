// Package generation manages the weka_runtime generation file used for takeover detection.
// Mirrors write_generation, obtain_lock, is_wrong_generation, get_boot_id at weka_runtime.py:2776–4344.
package generation

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	"github.com/weka/weka-operator/internal/runtime/config"
)

const (
	wekaK8sRuntimeDir = "/opt/weka/k8s-runtime"
	generationPath    = "/opt/weka/k8s-runtime/runtime-generation"
	persistencyMarker = "/opt/weka/k8s-runtime/persistency-configured"
	persistBindsDir   = "/host-binds/opt-weka"
)

// currentGeneration is set once at program start as a float-like string (matching Python str(time.time())).
var currentGeneration = fmt.Sprintf("%f", float64(time.Now().UnixNano())/1e9)

// Write waits for persistency to be configured (if needed), then writes the current generation.
// Mirrors Python write_generation() at weka_runtime.py:3290.
func Write(ctx context.Context, _ *config.Config) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "generation.Write")
	defer logger.End()

	// Wait while /host-binds/opt-weka exists but persistency is not yet configured.
	if err := cmdutil.PollUntil(ctx, 1*time.Second, func() bool {
		_, errBinds := os.Stat(persistBindsDir)
		_, errMarker := os.Stat(persistencyMarker)
		if os.IsNotExist(errBinds) || errMarker == nil {
			return true
		}
		logger.Info("Waiting for persistency to be configured")
		return false
	}); err != nil {
		return fmt.Errorf("generation.Write: waiting for persistency: %w", err)
	}

	logger.Info("Writing generation", "generation", currentGeneration)
	if err := os.MkdirAll(wekaK8sRuntimeDir, 0o755); err != nil {
		return fmt.Errorf("generation.Write mkdir: %w", err)
	}
	if err := os.WriteFile(generationPath, []byte(currentGeneration), 0o644); err != nil {
		return fmt.Errorf("generation.Write: %w", err)
	}
	return nil
}

// ObtainLock binds an abstract-namespace UNIX socket to provide an exclusive runtime lock.
// Mirrors Python obtain_lock() at weka_runtime.py:3312.
func ObtainLock(name string) (net.PacketConn, error) {
	return net.ListenPacket("unixgram", "\x00weka_runtime_"+name)
}

// IsWrongGeneration returns true when the on-disk generation differs from the current process.
// Mirrors Python is_wrong_generation() at weka_runtime.py:4325.
func IsWrongGeneration(cfg *config.Config) bool {
	switch cfg.Mode {
	case "drivers-loader", "discovery", "drivers-builder":
		return false
	}

	content, err := os.ReadFile(generationPath)
	if err != nil || len(content) == 0 {
		return false
	}
	onDisk := strings.TrimSpace(string(content))
	if onDisk == currentGeneration {
		return false
	}
	// Log at error level (non-fatal — caller decides what to do).
	fmt.Fprintf(os.Stderr, "generation mismatch: expected %s got %s\n", currentGeneration, onDisk)
	return true
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
