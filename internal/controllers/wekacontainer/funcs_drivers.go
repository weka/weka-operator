// This file contains functions related to drivers loading and building during WekaContainer reconciliation
package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"go.opentelemetry.io/otel/codes"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/pkg/util"
)

func (r *containerReconcilerLoop) EnsureDrivers(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	if r.node == nil {
		return errors.New("node not found")
	}

	details := r.container.ToOwnerDetails()
	if r.container.Spec.DriversLoaderImage == "" && r.IsNotAlignedImage() {
		// do not create pod with spec image if we know in advance that we cannot upgrade
		canUpgrade, err := r.upgradeConditionsPass(ctx)
		if err != nil || !canUpgrade {
			logger.Debug("Cannot upgrade to new image, using last applied", "image", details.Image, "error", err)
			details.Image = r.container.Status.LastAppliedImage
		}
	}

	if !operations.DriversLoaded(r.node, details.Image, r.container.HasFrontend()) {
		err := r.updateStatusWaitForDrivers(ctx)
		if err != nil {
			return err
		}
	} else {
		return nil
	}

	logger.Info("Loading drivers", "image", details.Image)

	driversLoader := operations.NewLoadDrivers(r.Manager, r.node, *details, r.container.Spec.DriversLoaderImage,
		r.container.Spec.DriversDistService, r.container.HasFrontend(), false)
	err := operations.ExecuteOperation(ctx, driversLoader)
	if err != nil {
		return err
	}
	return nil
}

func (r *containerReconcilerLoop) driversLoaded(ctx context.Context) (bool, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "driversLoaded")
	defer end()

	if !r.container.RequiresDrivers() {
		return true, nil
	}

	pod := r.pod
	timeout := 15 * time.Second

	executor, err := util.NewExecInPodWithTimeout(r.RestClient, r.Manager.GetConfig(), pod, &timeout)
	if err != nil {
		logger.Error(err, "Error creating executor")
		return false, err
	}
	stdout, stderr, err := executor.ExecNamed(ctx, "CheckDriversLoaded", []string{"bash", "-ce", "cat /tmp/weka-drivers.log"})
	if err != nil {
		// also verify that /opt/weka/k8s-runtime/resources.json exists and is correct
		if meta.IsStatusConditionTrue(r.container.Status.Conditions, condition.CondContainerResourcesWritten) {
			expectedAllocations, err2 := r.getExpectedAllocations(ctx)
			if err2 != nil {
				return false, fmt.Errorf("error getting expected allocations: %v, original err: %v", err2, err)
			}

			err2 = r.verifyResourcesJson(ctx, executor, expectedAllocations)
			if err2 != nil {
				if strings.Contains(err2.Error(), "context deadline exceeded") {
					return false, lifecycle.NewWaitErrorWithDuration(err2, time.Second*10)
				}

				err2 = fmt.Errorf("error checking resources.json: %v, original err: %v", err2, err)

				logger.Error(err2, "resources.json is incorrect, re-writing it")

				err3 := r.WriteResources(ctx)
				if err3 != nil {
					err3 = fmt.Errorf("error writing resources.json: %v, prev. error %v", err3, err2)
					return false, err3
				}
			}

		}
		return false, fmt.Errorf("error checking drivers loaded: %v, %s", err, stderr.String())
	}

	missingDriverName := strings.TrimSpace(stdout.String())

	if missingDriverName == "" {
		logger.InfoWithStatus(codes.Ok, "Drivers already loaded")
		return true, nil
	}

	logger.Info("Driver not loaded", "missing_driver", missingDriverName)
	return false, nil
}

type BuiltDriversResult struct {
	WekaVersion           string `json:"weka_version"`
	KernelSignature       string `json:"kernel_signature"`
	WekaPackNotSupported  bool   `json:"weka_pack_not_supported"`
	NoWekaDriversHandling bool   `json:"no_weka_drivers_handling"`
	Err                   string `json:"err"`
}

func (r *containerReconcilerLoop) UploadBuiltDrivers(ctx context.Context) error {
	targetDistContainer, err := r.getTargetContainer(ctx)
	if err != nil {
		return err
	}

	complete := func() error {
		r.container.Status.Status = weka.Completed
		return r.Status().Update(ctx, r.container)
	}

	// TODO: This is not a best solution, to download version, but, usable.
	// Should replace this with ad-hocy downloader container, that will use newer version(as the one who built), to download using shared storage

	executor, err := r.ExecService.GetExecutor(ctx, targetDistContainer)
	if err != nil {
		return err
	}

	builderIp := r.pod.Status.PodIP
	builderPort := r.container.GetPort()

	if builderIp == "" {
		return errors.New("Builder IP is not set")
	}

	results := &BuiltDriversResult{}
	err = json.Unmarshal([]byte(*r.container.Status.ExecutionResult), results)
	if err != nil {
		return err
	}

	if results.NoWekaDriversHandling {
		// for legacy drivers handling, we don't have support for weka driver command
		// copy everything from builer's /opt/weka/dist/drivers to targetDistcontainer's /opt/weka/dist/drivers
		cmd := fmt.Sprintf("cd /opt/weka/dist/drivers/ && wget -r -nH --cut-dirs=3 --no-parent --reject=\"index.html*\" http://%s:%d/dist/v1/drivers/", builderIp, builderPort)
		stdout, stderr, err := executor.ExecNamed(ctx, "CopyDrivers",
			[]string{"bash", "-ce", cmd},
		)
		if err != nil {
			err := fmt.Errorf("failed to run command: %s, error: %s, stdout: %s, stderr: %s", cmd, err, stdout.String(), stderr.String())
			return err
		}
		return complete()
	}

	endpoint := fmt.Sprintf("https://%s:%d", builderIp, builderPort)

	// if weka pack is not supported, we don't need to download it
	if !results.WekaPackNotSupported {
		stdout, stderr, err := executor.ExecNamed(ctx, "DownloadVersion",
			[]string{"bash", "-ce",
				"weka version get --driver-only " + results.WekaVersion + " --from " + endpoint,
			},
		)
		if err != nil {
			return errors.Wrap(err, stderr.String()+stdout.String())
		}
	}

	downloadCmd := "weka driver download --without-agent --version " + results.WekaVersion + " --from " + endpoint
	if !results.WekaPackNotSupported {
		downloadCmd += " --kernel-signature " + results.KernelSignature
	}

	stdout, stderr, err := executor.ExecNamed(ctx, "DownloadDrivers",
		[]string{"bash", "-ce", downloadCmd},
	)
	if err != nil {
		return errors.Wrap(err, stderr.String()+stdout.String())
	}

	if results.WekaPackNotSupported {
		url := fmt.Sprintf("%s/dist/v1/drivers/%s-%s.tar.gz.sha256", endpoint, results.WekaVersion, results.KernelSignature)
		cmd := "cd /opt/weka/dist/drivers/ && curl -kO " + url
		stdout, stderr, err = executor.ExecNamed(ctx, "Copy sha256 file",
			[]string{"bash", "-ce", cmd},
		)
		if err != nil {
			err := fmt.Errorf("failed to run command: %s, error: %s, stdout: %s, stderr: %s", cmd, err, stdout.String(), stderr.String())
			return err
		}
	}

	return complete()
}

func (r *containerReconcilerLoop) getTargetContainer(ctx context.Context) (*weka.WekaContainer, error) {
	target := r.container.Spec.UploadResultsTo
	if target == "" {
		return nil, errors.New("uploadResultsTo is not set")
	}

	targetDistContainer := &weka.WekaContainer{}
	// assuming same namespace
	err := r.Get(ctx, client.ObjectKey{Name: target, Namespace: r.container.Namespace}, targetDistContainer)
	if err != nil {
		return nil, errors.Wrap(err, "error getting target dist container")
	}

	return targetDistContainer, nil
}

func (r *containerReconcilerLoop) updateDriversBuilderStatus(ctx context.Context) error {
	return r.updateContainerStatusIfNotEquals(ctx, weka.Building)
}

// check if we actually can load drivers from dist service
// trigger re-build + re-upload if not
func (r *containerReconcilerLoop) uploadedDriversPeriodicCheck(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	if !r.container.IsDriversBuilder() {
		return nil
	}

	if r.container.Status.ExecutionResult == nil {
		logger.Debug("No execution result, skipping")
		return nil
	}

	results := &BuiltDriversResult{}
	err := json.Unmarshal([]byte(*r.container.Status.ExecutionResult), results)
	if err != nil {
		return err
	}

	logger.Info("Try loading drivers", "weka_version", results.WekaVersion, "kernel_signature", results.KernelSignature)

	targetDistContainer, err := r.getTargetContainer(ctx)
	if err != nil {
		return err
	}

	executor, err := r.ExecService.GetExecutor(ctx, targetDistContainer)
	if err != nil {
		return err
	}

	// assuming `weka driver pack` is supported
	downloadCmd := fmt.Sprintf(
		"weka driver download --without-agent --version %s --kernel-signature %s",
		results.WekaVersion, results.KernelSignature,
	)

	stdout, stderr, err := executor.ExecNamed(ctx, "DownloadDrivers",
		[]string{"bash", "-ce", downloadCmd},
	)
	if err != nil {
		err = fmt.Errorf("error downloading drivers: %w, stderr: %s", err, stderr.String())
		logger.Debug(err.Error())

		if strings.Contains(stderr.String(), "Failed to download the drivers") || strings.Contains(stderr.String(), "Version missing") {
			msg := "Cannot load drivers, trigger re-build and re-upload"
			logger.Info(msg)

			r.RecordEvent("", "DriversRebuild", msg)

			if err := r.clearStatus(ctx); err != nil {
				err = fmt.Errorf("error clearing builder results: %w", err)
				return err
			}
		}
		return err
	}

	logger.Debug("Drivers loaded successfully", "stdout", stdout.String())
	return nil
}
