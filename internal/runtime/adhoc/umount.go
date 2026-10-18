package adhoc

import (
	"context"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/results"
)

type umountResult struct {
	Error         []string `json:"error"`
	UmountedPaths []string `json:"umounted_paths"`
}

// RunUmount unmounts all active wekafs mounts in the host namespace,
// then attempts to remove the wekafsio kernel module.
// Matches Python umount_drivers() behaviour.
func RunUmount(ctx context.Context, _ *config.Config) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "RunUmount")
	defer logger.End()

	// 1. List wekafs mounts: 3rd whitespace-separated field of each line
	out, err := cmdutil.Output(ctx,
		"nsenter", "--mount", "--pid", "--target", "1", "--",
		"mount", "-t", "wekafs",
	)
	if err != nil {
		logger.Warn("umount: failed to list wekafs mounts", "err", err)
		// Proceed with empty mount list — write empty results
		return results.Write(umountResult{})
	}

	var errs []string
	var umounted []string

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// mount output: "device on mountpoint type ..." — 3rd field is mountpoint
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		mountPoint := fields[2]

		if umountErr := cmdutil.Run(ctx,
			"nsenter", "--mount", "--pid", "--target", "1", "--",
			"umount", mountPoint,
		); umountErr != nil {
			errs = append(errs, umountErr.Error())
			continue
		}
		umounted = append(umounted, mountPoint)
	}

	// 2. If no errors, attempt to remove the kernel module
	if len(errs) == 0 {
		if rmErr := cmdutil.Run(ctx,
			"nsenter", "--mount", "--pid", "--target", "1", "--",
			"rmmod", "wekafsio",
		); rmErr != nil {
			logger.Warn("umount: rmmod wekafsio failed (non-fatal)", "err", rmErr)
		}
	}

	logger.Info("umount complete", "umounted", len(umounted), "errors", len(errs))

	return results.Write(umountResult{
		Error:         errs,
		UmountedPaths: umounted,
	})
}
