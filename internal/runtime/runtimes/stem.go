package runtimes

import (
	"context"
	"fmt"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/weka"
)

// startStem brings up a minimal weka container with no backing agent-managed persistence:
// ensure it exists, configure traces, then start the stem CLI. Shared by drivers-dist and
// adhoc-op-with-container, the two modes that stand up such a container.
func startStem(ctx context.Context, deps *Deps, name string, port int, traces config.Traces, features domain.FeatureFlags) error {
	if err := weka.EnsureStemContainer(ctx, deps.Runner, name, port); err != nil {
		return fmt.Errorf("ensure stem container: %w", err)
	}
	if err := weka.ConfigureTraces(ctx, deps.Runner, weka.TracesInput{Mode: traces, Features: features}, name); err != nil {
		return fmt.Errorf("configure traces: %w", err)
	}
	if _, err := weka.StartStemCLI(deps.Coord.ServicesContext(), deps.Processes, name); err != nil {
		return fmt.Errorf("start stem container: %w", err)
	}
	return nil
}
