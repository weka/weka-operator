package device

import (
	"context"
	"os"
	"path/filepath"

	"github.com/kubevirt/device-plugin-manager/pkg/dpm"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	"k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

func NewDriveLister(logger *zap.SugaredLogger) *DriveLister {
	return &DriveLister{
		Logger: logger,
	}
}

// Drive Lister Implementation for DPM framework
type DriveLister struct {
	Logger *zap.SugaredLogger
}

//
// ListerInterface Implementation Methods
// See: https://pkg.go.dev/github.com/kubevirt/device-plugin-manager/pkg/dpm?utm_source=godoc#ListerInterface
//

func (d *DriveLister) GetResourceNamespace() string {
	d.Logger.Info("GetResourceNamespace")
	return "drive.weka.io"
}

func (d *DriveLister) Discover(pluginListCh chan dpm.PluginNameList) {
	d.Logger.Info("Discover")
	pluginListCh <- dpm.PluginNameList{
		"drive",
	}
}

func (d *DriveLister) NewPlugin(resourceName string) dpm.PluginInterface {
	d.Logger.Info("NewPlugin")
	return &Plugin{
		Logger: d.Logger,
	}
}

//
// DevicePluginServer Implementation Methods
// See: https://pkg.go.dev/k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1#DevicePluginServer
//

type Plugin struct {
	Logger *zap.SugaredLogger
}

func (p *Plugin) GetDevicePluginOptions(ctx context.Context, empty *v1beta1.Empty) (*v1beta1.DevicePluginOptions, error) {
	p.Logger.Info("GetDevicePluginOptions")
	return &v1beta1.DevicePluginOptions{}, nil
}

func (p *Plugin) ListAndWatch(empty *v1beta1.Empty, s v1beta1.DevicePlugin_ListAndWatchServer) error {
	p.Logger.Info("ListAndWatch")

	drives, err := ListDrives()
	if err != nil {
		return errors.Wrap(err, "failed to list drives")
	}

	devices := make([]*v1beta1.Device, len(drives))
	for i, drive := range drives {
		devices[i] = &v1beta1.Device{
			ID:     drive.Path,
			Health: v1beta1.Healthy,
		}
	}
	s.Send(&v1beta1.ListAndWatchResponse{Devices: devices})
	return nil
}

func (p *Plugin) GetPreferredAllocation(ctx context.Context, request *v1beta1.PreferredAllocationRequest) (*v1beta1.PreferredAllocationResponse, error) {
	p.Logger.Info("GetPreferredAllocation")
	return &v1beta1.PreferredAllocationResponse{}, nil
}

func (p *Plugin) Allocate(ctx context.Context, request *v1beta1.AllocateRequest) (*v1beta1.AllocateResponse, error) {
	p.Logger.Info("Allocate")
	return &v1beta1.AllocateResponse{}, nil
}

func (p *Plugin) PreStartContainer(ctx context.Context, request *v1beta1.PreStartContainerRequest) (*v1beta1.PreStartContainerResponse, error) {
	p.Logger.Info("PreStartContainer")
	return &v1beta1.PreStartContainerResponse{}, nil
}

//
// Drive Management Implementation
//

type Drive struct {
	// Name is the name of the drive.
	Name string
	// Path is the path to the drive.
	Path string
	UUID string
}

// ListDrives lists the drives available to the device plugin.
// Drives are limited to nvme drives returned by `blkid`.
func ListDrives() ([]Drive, error) {
	pattern := "/dev/nvme*"
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return []Drive{}, errors.Wrapf(err, "failed to list drives under %s", pattern)
	}
	if err != nil {
		return []Drive{}, errors.Wrapf(err, "failed to list drives under %s", pattern)
	}

	if len(matches) == 0 {
		return []Drive{}, errors.Errorf("no drives found under %s", pattern)
	}

	drives := make([]Drive, 0, len(matches))
	// var blkdIdErr error
	for _, match := range matches {
		// Use blkid to get the UUID of the drive.
		//cmd := exec.Command("blkid", match)
		//err := cmd.Run(); err != nil {
		//blkdIdErr = multierror.Append(blkdIdErr, err)
		//continue
		//}

		// drives with the partition file are partitions and should be skipped.
		partitionPath := match + "/partition"
		if _, err := os.Stat(partitionPath); err == nil {
			continue
		}

		drive := Drive{
			Name: filepath.Base(match),
			Path: match,
			UUID: "",
		}
		drives = append(drives, drive)
	}

	return drives, nil
}
