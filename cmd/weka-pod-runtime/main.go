package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	obslogger "github.com/weka/go-weka-observability/logger"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/modes"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)

	cfg := config.Load()

	logger := obslogger.NewZerologrWithLoggerNameInsteadCaller()
	ctx = obslogger.ContextWithLogr(ctx, logger)

	shutdown, err := instrumentation.SetupOTelSDKWithOptions(ctx, "weka-pod-runtime", cfg.Version, logger)
	if err != nil {
		// observability is non-critical, log and continue
		logger.Info("failed to set up OTel SDK", "err", err)
	}

	modeErr := modes.Run(ctx, cfg)

	if shutdown != nil {
		if shutdownErr := shutdown(ctx); shutdownErr != nil {
			logger.Info("failed to shutdown OTel", "err", shutdownErr)
		}
	}
	stop()

	// Mirror Python debug-sleep at weka_runtime.py:4655-4661:
	//   debug_sleep = int(WEKA_OPERATOR_DEBUG_SLEEP or 3)
	//   start = now; while now-start < debug_sleep: if /tmp/.cancel-debug-sleep: break; sleep(1)
	// i.e. poll the cancel file once per second so an externally-created flag aborts the sleep.
	debugSleep := cfg.DebugSleep
	if debugSleep == 0 {
		debugSleep = 3
	}
	logger.Info("debug sleep before exit", "seconds", debugSleep)
	for i := 0; i < debugSleep; i++ {
		if _, err := os.Stat("/tmp/.cancel-debug-sleep"); err == nil {
			logger.Info("debug sleep cancelled by /tmp/.cancel-debug-sleep")
			break
		}
		time.Sleep(1 * time.Second)
	}

	if modeErr != nil {
		logger.Error(modeErr, "mode failed", "mode", cfg.Mode)
		os.Exit(1)
	}
}
