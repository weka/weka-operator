package deviceplugin

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"

	"github.com/weka/weka-operator/pkg/util"
)

// DefaultKubeletDevicePluginsDir is the standard kubelet directory holding kubelet.sock and
// every device plugin's own registration socket.
const DefaultKubeletDevicePluginsDir = "/var/lib/kubelet/device-plugins"

// DefaultSysfsRoot is the standard sysfs mount point.
const DefaultSysfsRoot = "/sys"

// kubeletSocketName is the well-known file name of the kubelet registration socket within
// the device plugins directory.
const kubeletSocketName = "kubelet.sock"

// registrationTimeout bounds a single Register RPC call to kubelet.
const registrationTimeout = 10 * time.Second

// ManagerConfig configures a Manager.
type ManagerConfig struct {
	// DevicePluginDir is the kubelet device-plugins directory, containing kubelet.sock and
	// where this manager creates one socket per NUMA region. Defaults to
	// DefaultKubeletDevicePluginsDir.
	DevicePluginDir string
	// SysfsRoot is the sysfs mount point NUMA topology is discovered under (sysfsRoot +
	// "/devices/system/node"). Defaults to DefaultSysfsRoot.
	SysfsRoot string
	// RetryBackoff is how long Run waits before retrying after a failed cycle, and how long
	// it waits before restarting plugins after a detected kubelet restart. Defaults to 5s.
	RetryBackoff time.Duration
}

func (c ManagerConfig) withDefaults() ManagerConfig {
	if c.DevicePluginDir == "" {
		c.DevicePluginDir = DefaultKubeletDevicePluginsDir
	}
	if c.SysfsRoot == "" {
		c.SysfsRoot = DefaultSysfsRoot
	}
	if c.RetryBackoff <= 0 {
		c.RetryBackoff = 5 * time.Second
	}
	return c
}

// Manager discovers NUMA regions, runs one Plugin per region, registers each with kubelet,
// and re-registers them whenever kubelet restarts. It is best-effort: Run never returns an
// error to its caller, it logs and retries with backoff instead, so a device-plugin failure
// never brings down the node agent process hosting it.
type Manager struct {
	cfg    ManagerConfig
	logger logr.Logger

	mu      sync.Mutex
	plugins map[int]*Plugin

	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
}

// NewManager creates a Manager. cfg fields left at their zero value take the defaults
// documented on ManagerConfig.
func NewManager(cfg ManagerConfig, logger logr.Logger) *Manager {
	return &Manager{
		cfg:    cfg.withDefaults(),
		logger: logger.WithName("device-plugin-manager"),
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Run discovers NUMA regions, serves and registers a device plugin per region, then blocks
// watching for a kubelet restart. On restart (or any startup failure) it tears down and
// retries after RetryBackoff, until ctx is cancelled or Stop is called. Run always returns
// once stopped; it does not return an error, since failures here must never be fatal to the
// process hosting the manager.
func (m *Manager) Run(ctx context.Context) {
	defer close(m.doneCh)

	for {
		if err := m.runCycle(ctx); err != nil {
			m.logger.Error(err, "device plugin cycle failed, will retry")
		}

		select {
		case <-ctx.Done():
			m.stopAllPlugins()
			return
		case <-m.stopCh:
			m.stopAllPlugins()
			return
		case <-time.After(m.cfg.RetryBackoff):
		}
	}
}

// Stop signals Run to tear down all plugins and return, and blocks until it has done so.
// Safe to call multiple times and safe to call even if Run was never started.
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
	<-m.doneCh
}

// runCycle discovers regions, serves+registers a plugin per region, then blocks until a
// kubelet restart is observed or ctx/Stop fires, tearing down plugins before returning in
// every case so the next cycle always starts clean.
func (m *Manager) runCycle(ctx context.Context) error {
	regions, err := DiscoverNumaRegions(numaNodeDirFromSysfsRoot(m.cfg.SysfsRoot))
	if err != nil {
		return errors.Wrap(err, "failed to discover NUMA regions")
	}
	if len(regions) == 0 {
		m.logger.Info("no NUMA regions discovered, nothing to advertise")
	}

	plugins := make(map[int]*Plugin, len(regions))
	defer func() {
		// Any plugin left in `plugins` but not yet swapped into m.plugins on a failure
		// path is stopped here; the happy path clears `plugins` after the swap below.
		for _, p := range plugins {
			p.Stop()
		}
	}()

	for _, region := range regions {
		plugin := NewPlugin(region, filepath.Join(m.cfg.DevicePluginDir, SocketName(region)), m.logger)
		if err := plugin.Serve(); err != nil {
			return errors.Wrapf(err, "failed to serve device plugin for region %d", region)
		}
		plugins[region] = plugin

		if err := m.register(ctx, plugin); err != nil {
			return errors.Wrapf(err, "failed to register device plugin for region %d with kubelet", region)
		}
	}

	m.mu.Lock()
	m.plugins = plugins
	m.mu.Unlock()
	plugins = nil // ownership moved to m.plugins; the deferred cleanup above must not stop them

	m.logger.Info("device plugins registered", "regions", regions)

	m.watchForKubeletRestart(ctx)
	m.stopAllPlugins()
	return nil
}

// register dials the kubelet registration socket and registers plugin's resource.
func (m *Manager) register(ctx context.Context, plugin *Plugin) error {
	kubeletSocket := filepath.Join(m.cfg.DevicePluginDir, kubeletSocketName)

	conn, err := grpc.NewClient("unix://"+kubeletSocket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return errors.Wrapf(err, "failed to create client for kubelet socket %s", kubeletSocket)
	}
	defer conn.Close() //nolint:errcheck // registration is a one-shot call, connection not reused

	dialCtx, cancel := context.WithTimeout(ctx, registrationTimeout)
	defer cancel()

	client := pluginapi.NewRegistrationClient(conn)
	_, err = client.Register(dialCtx, &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     filepath.Base(plugin.SocketPath),
		ResourceName: plugin.ResourceName,
		Options: &pluginapi.DevicePluginOptions{
			PreStartRequired:                false,
			GetPreferredAllocationAvailable: false,
		},
	})
	if err != nil {
		return errors.Wrap(err, "Register RPC failed")
	}

	m.logger.Info("registered device plugin with kubelet", "region", plugin.Region, "resourceName", plugin.ResourceName, "endpoint", filepath.Base(plugin.SocketPath))
	return nil
}

// watchForKubeletRestart blocks until it observes a signal that kubelet has restarted (its
// registration socket, or one of our own device plugin sockets, was removed or recreated),
// or until ctx is done or Stop is called. It always returns nil; callers re-enter runCycle
// to serve fresh sockets and re-register.
func (m *Manager) watchForKubeletRestart(ctx context.Context) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		m.logger.Error(err, "failed to create fsnotify watcher, falling back to polling")
		m.pollForKubeletRestart(ctx)
		return
	}
	defer watcher.Close() //nolint:errcheck // best-effort cleanup

	if err := watcher.Add(m.cfg.DevicePluginDir); err != nil {
		m.logger.Error(err, "failed to watch device plugin dir, falling back to polling")
		m.pollForKubeletRestart(ctx)
		return
	}

	kubeletSocket := filepath.Join(m.cfg.DevicePluginDir, kubeletSocketName)

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Name == kubeletSocket && (event.Has(fsnotify.Remove) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename)) {
				m.logger.Info("kubelet socket changed, restarting device plugins", "event", event.String())
				return
			}
			if event.Has(fsnotify.Remove) && m.isOwnSocket(event.Name) {
				m.logger.Info("device plugin socket removed, restarting device plugins", "event", event.String())
				return
			}
		case watchErr, ok := <-watcher.Errors:
			if !ok {
				return
			}
			m.logger.Error(watchErr, "fsnotify watcher error")
		}
	}
}

// pollForKubeletRestart is the fallback used when fsnotify is unavailable: it periodically
// checks that kubelet.sock and every plugin socket still exist.
func (m *Manager) pollForKubeletRestart(ctx context.Context) {
	kubeletSocket := filepath.Join(m.cfg.DevicePluginDir, kubeletSocketName)
	ticker := time.NewTicker(m.cfg.RetryBackoff)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
			if !util.FileExists(kubeletSocket) || m.anyOwnSocketMissing() {
				return
			}
		}
	}
}

func (m *Manager) isOwnSocket(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.plugins {
		if p.SocketPath == name {
			return true
		}
	}
	return false
}

func (m *Manager) anyOwnSocketMissing() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.plugins {
		if !util.FileExists(p.SocketPath) {
			return true
		}
	}
	return false
}

func (m *Manager) stopAllPlugins() {
	m.mu.Lock()
	plugins := m.plugins
	m.plugins = nil
	m.mu.Unlock()

	for _, p := range plugins {
		p.Stop()
	}
}
