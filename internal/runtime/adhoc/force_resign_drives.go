package adhoc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/blockdev"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/results"
	"github.com/weka/weka-operator/internal/runtime/wekadrive"
)

// RunForceResignDrives implements the force-resign-drives adhoc instruction.
// It resolves device paths (from explicit paths or serials), signs them with
// AllowEraseWekaPartitions=true, then writes a ResignDrivesResult.
func RunForceResignDrives(ctx context.Context, cfg *config.Config) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "RunForceResignDrives")
	defer logger.End()

	var payload weka.ForceResignDrivesPayload
	if err := json.Unmarshal([]byte(cfg.Instructions.Payload), &payload); err != nil {
		return fmt.Errorf("force-resign-drives: unmarshal payload: %w", err)
	}

	var paths []string

	if len(payload.DevicePaths) > 0 {
		paths = payload.DevicePaths
	} else {
		for _, serial := range payload.DeviceSerials {
			p, err := blockdev.GetDevicePathBySerial(ctx, serial)
			if err != nil {
				logger.Info("force-resign-drives: failed to resolve serial to path, skipping", "serial", serial, "err", err.Error())
				continue
			}
			paths = append(paths, p)
		}
	}

	opts := &wekadrive.SignOptions{
		AllowEraseWekaPartitions: true,
	}

	signedPaths, err := wekadrive.SignBatch(ctx, paths, opts)
	if err != nil {
		return fmt.Errorf("force-resign-drives: SignBatch: %w", err)
	}

	return results.Write(domain.ResignDrivesResult{
		Drives: signedPaths,
	})
}
