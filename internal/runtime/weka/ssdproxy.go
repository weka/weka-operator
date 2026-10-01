package weka

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/paths"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// ErrUnsupportedSSDProxyRecovery is returned when an existing but stopped ssdproxy container has
// no recoverable weka-resources.*.json file to relink.
// TODO: recover by re-running "weka local setup ssdproxy" instead of returning this error.
var ErrUnsupportedSSDProxyRecovery = errors.New("ssdproxy: existing container has no recoverable resources file")

// SSDProxyInput carries SetupSSDProxyContainer's dependencies and configuration.
type SSDProxyInput struct {
	Runner      process.CommandRunner
	Clock       clock.Clock
	Roots       paths.Roots
	Features    domain.FeatureFlags
	Name        string
	MemoryBytes int64
	MemoryStr   string // original human-readable MEMORY value (e.g. "4Gi"), for the setup CLI flag
}

// SetupSSDProxyContainer creates the ssdproxy container if absent, recovers an existing but
// unhealthy one from its last-known resources file, then unconditionally restages
// resources.json (memory + reserve_1g_hugepages=false) and starts it.
// Mirrors Python ensure_ssdproxy_container() at weka_runtime.py:3838-3885.
func SetupSSDProxyContainer(ctx context.Context, in *SSDProxyInput) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "weka.SetupSSDProxyContainer", "container", in.Name)
	defer logger.End()

	resourcesDir := filepath.Join(in.Roots.OptWeka, "data", in.Name, "container")
	if err := os.MkdirAll(resourcesDir, 0o755); err != nil {
		return fmt.Errorf("SetupSSDProxyContainer: mkdir %s: %w", resourcesDir, err)
	}

	kernelizeScript := fmt.Sprintf(`ln -sf %s/dist/extracted/weka-sign-drive /usr/bin/weka-sign-drive
weka-sign-drive kernelize`, in.Roots.OptWeka)
	if _, err := in.Runner.Run(ctx, process.Shell(kernelizeScript)); err != nil {
		logger.Warn("weka-sign-drive kernelize failed (continuing)", "err", err)
	}

	containers, err := GetContainers(ctx, in.Runner)
	if err != nil {
		return fmt.Errorf("SetupSSDProxyContainer: list containers: %w", err)
	}
	found, findErr := findContainerByName(containers, in.Name)
	_ = findErr // nil, not-found is not an error here

	if found == nil {
		setupScript := fmt.Sprintf("weka local setup ssdproxy --memory=%s --base-port 13000 --enable-ssdproxy-nginx --no-start --disable", in.MemoryStr)
		if _, err = in.Runner.Run(ctx, process.Shell(setupScript)); err != nil {
			return fmt.Errorf("SetupSSDProxyContainer: setup: %w", err)
		}
	} else if err = recoverExistingSSDProxyContainer(ctx, found, resourcesDir); err != nil {
		return fmt.Errorf("SetupSSDProxyContainer: handle existing container: %w", err)
	}

	resBytes, err := GetWekaLocalResources(ctx, in.Runner, in.Name)
	if err != nil {
		return fmt.Errorf("SetupSSDProxyContainer: get resources: %w", err)
	}
	doc, err := ParseResourceDoc(resBytes)
	if err != nil {
		return fmt.Errorf("SetupSSDProxyContainer: parse resources: %w", err)
	}
	doc.SetMemory(in.MemoryBytes)
	doc.SetReserve1GHugepages(false)
	if err := WriteResourceDoc(ctx, resourcesDir, doc); err != nil {
		return fmt.Errorf("SetupSSDProxyContainer: write resources: %w", err)
	}

	return StartContainer(ctx, in.Runner, in.Name)
}

// recoverExistingSSDProxyContainer mirrors weka.HandleExistingContainer/checkResourcesJSON for
// the ssdproxy case: a running container or one with a non-empty resources.json needs no action;
// an empty resources.json is recovered by relinking the newest non-empty
// weka-resources.*.json candidate. Recreating from scratch (no candidate at all) is not
// implemented — see ErrUnsupportedSSDProxyRecovery.
func recoverExistingSSDProxyContainer(ctx context.Context, container map[string]interface{}, resourcesDir string) error {
	if running, ok := container["isRunning"].(bool); ok && running {
		return nil
	}
	if status, ok := container["runStatus"].(string); !ok || status != "Unknown" {
		return nil
	}

	recovered, err := relinkLatestResourcesFile(ctx, resourcesDir)
	if err != nil {
		return err
	}
	if !recovered {
		return ErrUnsupportedSSDProxyRecovery
	}
	return nil
}
