package modes

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/drivers"
	"github.com/weka/weka-operator/internal/runtime/results"
)

func init() {
	register("drivers-builder", runDriversBuilder)
}

type builderResult struct {
	DriverBuilt           bool   `json:"driver_built"`
	Err                   string `json:"err"`
	WekaVersion           string `json:"weka_version"`
	KernelBuildID         string `json:"kernel_build_id"`
	KernelSignature       string `json:"kernel_signature"`
	WekaPackNotSupported  bool   `json:"weka_pack_not_supported"`
	NoWekaDriversHandling bool   `json:"no_weka_drivers_handling"`
}

func runDriversBuilder(ctx context.Context, cfg *config.Config) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "modes.runDriversBuilder")
	defer logger.End()

	version, err := drivers.GetWekaVersion()
	if err != nil {
		return fmt.Errorf("runDriversBuilder: get weka version: %w", err)
	}
	logger.Info("building drivers", "version", version)

	versionGetCmd := fmt.Sprintf(
		"weka version get --driver-only --without-agent --no-progress-bar --from file://shared-weka-version/opt-weka %s",
		version,
	)
	if runErr := cmdutil.Run(ctx, "sh", "-c", versionGetCmd); runErr != nil {
		return fmt.Errorf("runDriversBuilder: weka version get: %w", runErr)
	}

	kernelBuildID, err := drivers.KernelBuildID(cfg.DriversBuildID, cfg.DistService)
	if err != nil {
		return fmt.Errorf("runDriversBuilder: %w", err)
	}

	packCmd := fmt.Sprintf("weka driver pack --without-agent --version %s", version)
	if kernelBuildID != "" {
		packCmd += " --kernel-build-id " + kernelBuildID
	}
	if runErr := cmdutil.Run(ctx, "sh", "-c", packCmd); runErr != nil {
		return fmt.Errorf("runDriversBuilder: weka driver pack: %w", runErr)
	}

	if mkdirErr := os.MkdirAll("/opt/weka/dist", 0o755); mkdirErr != nil {
		return fmt.Errorf("runDriversBuilder: mkdir /opt/weka/dist: %w", mkdirErr)
	}
	// v1 symlink makes GET /dist/v1/drivers/... resolve to /opt/weka/dist/drivers/...
	_ = os.Remove("/opt/weka/dist/v1") //nolint:errcheck // best-effort: absent is fine, Symlink below handles IsExist
	if symlinkErr := os.Symlink("/opt/weka/dist", "/opt/weka/dist/v1"); symlinkErr != nil && !os.IsExist(symlinkErr) {
		return fmt.Errorf("runDriversBuilder: symlink v1: %w", symlinkErr)
	}

	kernelSig, err := drivers.KernelSignature("/opt/weka/dist/drivers")
	if err != nil {
		return fmt.Errorf("runDriversBuilder: %w", err)
	}

	res := builderResult{
		DriverBuilt:           true,
		Err:                   "",
		WekaVersion:           version,
		KernelBuildID:         kernelBuildID,
		KernelSignature:       kernelSig,
		WekaPackNotSupported:  false,
		NoWekaDriversHandling: !drivers.WekaDriversHandling(cfg.ImageName),
	}
	if err := results.Write(res); err != nil {
		return fmt.Errorf("runDriversBuilder: write results: %w", err)
	}

	port := cfg.Port
	if port == 0 {
		port = 60002
	}
	logger.Info("starting HTTP file server", "port", port)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: http.FileServer(http.Dir("/opt/weka")),
	}

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		_ = srv.Shutdown(context.Background()) //nolint:errcheck // best-effort graceful shutdown on context cancellation
		return nil
	case err := <-serverErr:
		return fmt.Errorf("runDriversBuilder: http server: %w", err)
	}
}
