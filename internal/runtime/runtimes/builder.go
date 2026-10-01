package runtimes

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/osinfo"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/drivers"
	"github.com/weka/weka-operator/internal/runtime/process"
	"github.com/weka/weka-operator/internal/runtime/results"
)

type builderResult struct {
	DriverBuilt           bool   `json:"driver_built"`
	Err                   string `json:"err"`
	WekaVersion           string `json:"weka_version"`
	KernelBuildID         string `json:"kernel_build_id"`
	KernelSignature       string `json:"kernel_signature"`
	WekaPackNotSupported  bool   `json:"weka_pack_not_supported"`
	NoWekaDriversHandling bool   `json:"no_weka_drivers_handling"`
}

// runDriverBuilder builds and packs weka drivers for the image's version, then serves
// /opt/weka over HTTP so other pods can fetch the built dist.
// Mirrors Python drivers-builder mode at weka_runtime.py.
func runDriverBuilder(ctx context.Context, cfg *config.DriverBuilderConfig, deps *Deps) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "runtimes.runDriverBuilder")
	defer logger.End()

	nodeInfo, err := osinfo.Load()
	if err != nil {
		return fmt.Errorf("drivers-builder: load osinfo: %w", err)
	}
	if nodeInfo.IsNixos() {
		if err = drivers.PrepareNixosHostKernel(ctx, deps.Runner); err != nil {
			return fmt.Errorf("drivers-builder: %w", err)
		}
	}

	if err = runPreRunScript(ctx, deps, cfg.PreRunScript); err != nil {
		return fmt.Errorf("drivers-builder: pre-run script: %w", err)
	}

	version, err := drivers.GetWekaVersion(deps.Paths.OptWeka)
	if err != nil {
		return fmt.Errorf("drivers-builder: get weka version: %w", err)
	}
	logger.Info("building drivers", "version", version)

	kernelBuildID := drivers.BuilderKernelBuildID(nodeInfo)

	kernelSig, err := drivers.Build(ctx, deps.Runner, deps.Paths.OptWeka, version, kernelBuildID)
	if err != nil {
		return fmt.Errorf("drivers-builder: %w", err)
	}

	res := builderResult{
		DriverBuilt:           true,
		WekaVersion:           version,
		KernelBuildID:         kernelBuildID,
		KernelSignature:       kernelSig,
		NoWekaDriversHandling: !drivers.WekaDriversHandling(cfg.Drivers.ImageName),
	}
	return bindAndPublish(ctx, deps, cfg.ServePort, cfg.Results.Path, &res, logger)
}

// bindAndPublish binds the driver-serving port, writes results only once the bind succeeds,
// then serves /opt/weka over HTTP for the lifetime of the coordinator.
// Bind before writing results: a caller that sees results.json must be able to reach the
// server immediately after.
func bindAndPublish(ctx context.Context, deps *Deps, servePort int, resultsPath string, res *builderResult, logger *instrumentation.SpanLogger) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", servePort))
	if err != nil {
		return fmt.Errorf("drivers-builder: listen on port %d: %w", servePort, err)
	}

	if err := results.Write(ctx, resultsPath, res); err != nil {
		_ = ln.Close() //nolint:errcheck // best-effort: process is failing anyway
		return fmt.Errorf("drivers-builder: write results: %w", err)
	}

	logger.Info("starting HTTP file server", "port", servePort)
	srv := &http.Server{Handler: http.FileServer(http.Dir("/opt/weka"))}
	deps.Coord.Go("builder-http", func(taskCtx context.Context) error {
		stop := context.AfterFunc(taskCtx, func() {
			_ = srv.Close() //nolint:errcheck // best-effort: process is shutting down anyway
		})
		defer stop()

		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("http server: %w", err)
	})

	return nil
}

// runPreRunScript decodes and runs an optional base64-encoded shell script before the
// build starts. Mirrors Python run_prerun_script() at weka_runtime.py.
func runPreRunScript(ctx context.Context, deps *Deps, encoded string) error {
	if encoded == "" {
		return nil
	}
	script, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("decode PRE_RUN_SCRIPT: %w", err)
	}
	path := filepath.Join(deps.Paths.Tmp, "pre-run-script.sh")
	if err := os.WriteFile(path, script, 0o755); err != nil {
		return fmt.Errorf("write pre-run script: %w", err)
	}
	if _, err := deps.Runner.Run(ctx, process.Command{Path: "bash", Args: []string{path}}); err != nil {
		return fmt.Errorf("run pre-run script: %w", err)
	}
	return nil
}
