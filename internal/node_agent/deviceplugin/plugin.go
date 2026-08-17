package deviceplugin

import (
	"context"
	"net"
	"os"
	"strconv"
	"sync"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// WekaNumaRegionEnv is the environment variable set in a container's environment on
// Allocate, telling it which NUMA region the allocated device(s) belong to.
const WekaNumaRegionEnv = "WEKA_NUMA_REGION"

// Plugin implements the kubelet device plugin gRPC API (pluginapi.DevicePluginServer) for a
// single NUMA region. It advertises a fixed set of DevicesPerRegion virtual devices under
// ResourceName(Region) and serves them on its own unix socket, SocketPath. Registration with
// kubelet is handled separately by Manager, not by Plugin itself.
type Plugin struct {
	pluginapi.UnimplementedDevicePluginServer

	Region       int
	ResourceName string
	SocketPath   string

	logger logr.Logger

	mu       sync.Mutex
	server   *grpc.Server
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewPlugin creates a Plugin for the given NUMA region, serving on socketPath (an absolute
// path under the kubelet device-plugins directory).
func NewPlugin(region int, socketPath string, logger logr.Logger) *Plugin {
	return &Plugin{
		Region:       region,
		ResourceName: ResourceName(region),
		SocketPath:   socketPath,
		logger:       logger.WithValues("region", region, "resourceName", ResourceName(region)),
		stopCh:       make(chan struct{}),
	}
}

// Serve starts the plugin's gRPC server listening on SocketPath. Any stale socket file left
// behind by a previous run is removed first. Serve returns once the listener is up; the gRPC
// server itself runs in a background goroutine until Stop is called.
func (p *Plugin) Serve() error {
	if err := os.Remove(p.SocketPath); err != nil && !os.IsNotExist(err) {
		return errors.Wrapf(err, "failed to remove stale device plugin socket %s", p.SocketPath)
	}

	listener, err := net.Listen("unix", p.SocketPath)
	if err != nil {
		return errors.Wrapf(err, "failed to listen on device plugin socket %s", p.SocketPath)
	}

	server := grpc.NewServer()
	pluginapi.RegisterDevicePluginServer(server, p)

	p.mu.Lock()
	p.server = server
	p.mu.Unlock()

	go func() {
		if serveErr := server.Serve(listener); serveErr != nil {
			p.logger.V(1).Info("device plugin gRPC server stopped", "error", serveErr.Error())
		}
	}()

	return nil
}

// Stop gracefully stops the gRPC server, unblocks any in-flight ListAndWatch stream, and
// removes the socket file. Safe to call multiple times.
func (p *Plugin) Stop() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
	})

	p.mu.Lock()
	server := p.server
	p.mu.Unlock()

	if server != nil {
		server.Stop()
	}

	if err := os.Remove(p.SocketPath); err != nil && !os.IsNotExist(err) {
		p.logger.V(1).Info("failed to remove device plugin socket on stop", "error", err.Error())
	}
}

// GetDevicePluginOptions reports that this plugin needs neither PreStartContainer calls nor
// GetPreferredAllocation calls: device health never changes and any of the DevicesPerRegion
// slots is as good as any other.
func (p *Plugin) GetDevicePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{
		PreStartRequired:                false,
		GetPreferredAllocationAvailable: false,
	}, nil
}

// ListAndWatch streams the initial (fixed) device list, then blocks until the plugin is
// stopped. Devices never change health or disappear during the plugin's lifetime, so no
// further updates are ever sent.
func (p *Plugin) ListAndWatch(_ *pluginapi.Empty, stream pluginapi.DevicePlugin_ListAndWatchServer) error {
	if err := stream.Send(&pluginapi.ListAndWatchResponse{Devices: GenerateDevices(p.Region)}); err != nil {
		return errors.Wrap(err, "failed to send initial device list")
	}

	<-p.stopCh
	return nil
}

// GetPreferredAllocation is a no-op: GetDevicePluginOptions advertises
// GetPreferredAllocationAvailable=false, so kubelet is not expected to call this, but the
// interface must still be implemented.
func (p *Plugin) GetPreferredAllocation(context.Context, *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	return &pluginapi.PreferredAllocationResponse{}, nil
}

// PreStartContainer is a no-op: GetDevicePluginOptions advertises PreStartRequired=false, so
// kubelet is not expected to call this, but the interface must still be implemented.
func (p *Plugin) PreStartContainer(context.Context, *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}

// Allocate is called by kubelet during container creation for each container requesting
// devices from ResourceName. It sets WEKA_NUMA_REGION in the container's environment so the
// workload knows which region it was allocated against.
func (p *Plugin) Allocate(_ context.Context, req *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	resp := &pluginapi.AllocateResponse{
		ContainerResponses: make([]*pluginapi.ContainerAllocateResponse, 0, len(req.GetContainerRequests())),
	}
	for range req.GetContainerRequests() {
		resp.ContainerResponses = append(resp.ContainerResponses, &pluginapi.ContainerAllocateResponse{
			Envs: map[string]string{
				WekaNumaRegionEnv: strconv.Itoa(p.Region),
			},
		})
	}
	return resp, nil
}
