package modes

import (
	"context"
	"fmt"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/config"
)

type ModeFunc func(ctx context.Context, cfg *config.Config) error

var registry = map[string]ModeFunc{}

// oneShotModes are run-once operations that perform a task, write /weka-runtime/results.json,
// and then must keep the container alive until the operator deletes the pod (which delivers
// SIGTERM). This mirrors Python's loop.run_forever() at weka_runtime.py:4742: after main()
// completes the work it does NOT exit — it blocks until signalled.
//
// Exiting immediately is a regression: these pods inherit the default RestartPolicy=Always
// (resources/pod.go sets none), so kubelet restarts the completed container, which re-runs the
// operation and exits again — producing CrashLoopBackOff even though the work succeeded. Staying
// alive also lets the operator exec in to read results.json before deleting the pod.
//
// Only the success path blocks. If the mode returns an error, Run propagates it and the process
// exits non-zero, matching Python where an exception in main() skips run_forever().
var oneShotModes = map[string]bool{
	"adhoc-op":                true,
	"adhoc-op-with-container": true,
	"discovery":               true,
	"drivers-loader":          true,
}

func register(name string, fn ModeFunc) {
	registry[name] = fn
}

func Run(ctx context.Context, cfg *config.Config) error {
	fn, ok := registry[cfg.Mode]
	if !ok {
		return fmt.Errorf("unknown mode: %q", cfg.Mode)
	}
	if err := fn(ctx, cfg); err != nil {
		return err
	}
	if oneShotModes[cfg.Mode] {
		awaitShutdown(ctx)
	}
	return nil
}

// awaitShutdown blocks until ctx is cancelled (SIGTERM/SIGINT from the operator deleting the pod).
// See oneShotModes for why run-once modes must stay alive after completing their work.
func awaitShutdown(ctx context.Context) {
	_, logger := instrumentation.CreateLogSpan(ctx, "modes.awaitShutdown")
	defer logger.End()

	logger.Info("operation complete; awaiting shutdown signal (keeping container alive for results retrieval)")
	<-ctx.Done()
	logger.Info("shutdown signal received; exiting")
}
