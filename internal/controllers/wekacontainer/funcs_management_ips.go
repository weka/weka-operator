package wekacontainer

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/pkg/util"
)

func (r *containerReconcilerLoop) getManagementIps(ctx context.Context) ([]string, error) {
	executor, err := r.ExecService.GetExecutor(ctx, r.container)
	if err != nil {
		return nil, err
	}

	stdout, stderr, err := executor.ExecNamed(ctx, "GetManagementIps", []string{"cat", "/opt/weka/k8s-runtime/management_ips"})
	if err != nil {
		err = fmt.Errorf("Error reading management IPs: %v, %s", err, stderr.String())
		return nil, err
	}

	ips := strings.Split(strings.TrimSpace(stdout.String()), "\n")

	return ips, nil
}

func (r *containerReconcilerLoop) reconcileManagementIPs(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container

	ipAddresses, err := r.getManagementIps(ctx)
	if err != nil {
		err = errors.New("waiting for management IPs")
		return err
	}

	logger.WithValues("management_ips", ipAddresses).Info("Got management IPs")
	if !util.SliceEquals(container.Status.ManagementIPs, ipAddresses) {
		container.Status.ManagementIPs = ipAddresses

		r.container.Status.PrinterColumns.SetManagementIps(ipAddresses)
		if err := r.Status().Update(ctx, container); err != nil {
			logger.Error(err, "Error updating status")
			return err
		}
		return nil
	}

	return nil
}
