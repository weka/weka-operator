package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

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
	if err := modes.Run(ctx, cfg); err != nil {
		logger.Error(err, "mode failed", "mode", cfg.Mode)
		if shutdown != nil {
			if shutdownErr := shutdown(ctx); shutdownErr != nil {
				logger.Info("failed to shutdown OTel", "err", shutdownErr)
			}
		}
		stop()
		os.Exit(1)
	}
	if shutdown != nil {
		if shutdownErr := shutdown(ctx); shutdownErr != nil {
			logger.Info("failed to shutdown OTel", "err", shutdownErr)
		}
	}
	stop()
}
