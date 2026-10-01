package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	obslogger "github.com/weka/go-weka-observability/logger"
	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/debugexit"
	"github.com/weka/weka-operator/internal/runtime/lifecycle"
	"github.com/weka/weka-operator/internal/runtime/logrotate"
	"github.com/weka/weka-operator/internal/runtime/paths"
	"github.com/weka/weka-operator/internal/runtime/process"
	"github.com/weka/weka-operator/internal/runtime/runtimes"
)

func main() {
	logger := obslogger.NewZerologrWithLoggerNameInsteadCaller()
	// root is the only never-cancelled context in the process; the coordinator built on it
	// installs its own signal handling and cancels its derived contexts on shutdown.
	root := obslogger.ContextWithLogr(context.Background(), logger)

	env := config.CaptureEnv()
	rt, err := config.ParseRuntimeSection(env)
	if err != nil {
		logger.Error(err, "parse runtime config")
		os.Exit(1)
	}

	otelShutdown, err := instrumentation.SetupOTelSDKWithOptions(root, "weka-pod-runtime", rt.BinaryVersion, logger)
	if err != nil {
		// observability is non-critical, log and continue
		logger.Info("failed to set up OTel SDK", "err", err)
	}

	pm := process.NewManager(root)
	coord := lifecycle.New(root, pm)
	// Keep SIGTERM/SIGINT caught for the whole process, not only inside Run: teardown can deliver
	// one during the flush and exit wait below, which would otherwise kill the process with 143.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for range sigs {
			coord.RequestShutdown(lifecycle.ReasonSignal)
		}
	}()
	deps := &runtimes.Deps{
		Runner:    pm,
		Processes: pm,
		Clock:     clock.System,
		Paths:     paths.Default(),
		Coord:     coord,
	}

	modeRT, err := runtimes.New(env, deps)
	if err != nil {
		logger.Error(err, "build mode runtime")
		os.Exit(1)
	}

	if logrotate.Applies(rt.Mode, env.Get("SYSLOG_PACKAGE")) {
		coord.GoPeriodic("logrotate", deps.Clock, 0, logrotate.RotateInterval, func(ctx context.Context) error {
			return logrotate.Rotate(ctx, deps.Runner)
		})
	}

	outcome := coord.Run(root, modeRT)
	if outcome.StartupErr != nil {
		logger.Error(outcome.StartupErr, "mode failed", "mode", rt.Mode)
	}
	if outcome.ShutdownErr != nil {
		logger.Error(outcome.ShutdownErr, "shutdown failed", "mode", rt.Mode)
	}
	for _, cerr := range outcome.CleanupErrs {
		logger.Info("cleanup error", "err", cerr)
	}

	if otelShutdown != nil {
		flushCtx, cancelFlush := context.WithTimeout(root, 5*time.Second)
		if err := otelShutdown(flushCtx); err != nil {
			logger.Info("failed to shutdown OTel", "err", err)
		}
		cancelFlush()
	}

	debugexit.Wait(root, deps.Clock, deps.Paths.Tmp, rt.DebugExitWait)

	os.Exit(outcome.ExitCode())
}
