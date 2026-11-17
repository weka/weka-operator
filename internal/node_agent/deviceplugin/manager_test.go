package deviceplugin

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// fakeRegistrationServer records every Register call it receives, standing in for kubelet's
// real Registration gRPC service in tests.
type fakeRegistrationServer struct {
	pluginapi.UnimplementedRegistrationServer

	mu    sync.Mutex
	calls []*pluginapi.RegisterRequest
}

func (f *fakeRegistrationServer) Register(_ context.Context, req *pluginapi.RegisterRequest) (*pluginapi.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	return &pluginapi.Empty{}, nil
}

func (f *fakeRegistrationServer) recordedCalls() []*pluginapi.RegisterRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*pluginapi.RegisterRequest(nil), f.calls...)
}

// startFakeKubelet serves a fakeRegistrationServer on kubelet.sock in dir until the test ends.
func startFakeKubelet(t *testing.T, dir string) *fakeRegistrationServer {
	t.Helper()
	listener, err := net.Listen("unix", filepath.Join(dir, kubeletSocketName))
	if err != nil {
		t.Fatalf("failed to listen on fake kubelet socket: %v", err)
	}
	fakeServer := &fakeRegistrationServer{}
	grpcServer := grpc.NewServer()
	pluginapi.RegisterRegistrationServer(grpcServer, fakeServer)
	go grpcServer.Serve(listener) //nolint:errcheck // test server, shut down via Stop() in cleanup
	t.Cleanup(grpcServer.Stop)
	return fakeServer
}

func TestManager_RegistersAllRegionPluginsWithKubelet(t *testing.T) {
	sysfsRoot := t.TempDir()
	nodeDir := filepath.Join(sysfsRoot, "devices", "system", "node")
	for _, name := range []string{"node0", "node1"} {
		if err := os.MkdirAll(filepath.Join(nodeDir, name), 0755); err != nil {
			t.Fatalf("failed to create %s: %v", name, err)
		}
	}

	devicePluginDir := shortTempDir(t)
	fakeServer := startFakeKubelet(t, devicePluginDir)

	manager := NewManager(ManagerConfig{
		DevicePluginDir: devicePluginDir,
		SysfsRoot:       sysfsRoot,
		RetryBackoff:    50 * time.Millisecond,
	}, logr.Discard())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go manager.Run(ctx)
	defer manager.Stop()

	waitUntil(t, "2 registrations", func() bool { return len(fakeServer.recordedCalls()) >= 2 })

	calls := fakeServer.recordedCalls()
	if len(calls) != 2 {
		t.Fatalf("got %d registration calls, want 2", len(calls))
	}

	byResource := make(map[string]*pluginapi.RegisterRequest, len(calls))
	for _, c := range calls {
		byResource[c.ResourceName] = c
	}

	for _, region := range []int{0, 1} {
		req, ok := byResource[ResourceName(region)]
		if !ok {
			t.Fatalf("no registration found for %s", ResourceName(region))
		}
		wantSocket := SocketName(region, "")
		if req.Endpoint != wantSocket {
			t.Errorf("region %d endpoint = %q, want %q", region, req.Endpoint, wantSocket)
		}
		if req.Version != pluginapi.Version {
			t.Errorf("region %d version = %q, want %q", region, req.Version, pluginapi.Version)
		}
		if _, err := os.Stat(filepath.Join(devicePluginDir, wantSocket)); err != nil {
			t.Errorf("expected device plugin socket for region %d to exist: %v", region, err)
		}
	}
}

// TestManager_StopIsIdempotent verifies Stop can be called more than once without blocking
// or panicking, once Run has actually started (Stop blocks on doneCh, which only Run closes).
func TestManager_StopIsIdempotent(t *testing.T) {
	manager := NewManager(ManagerConfig{
		DevicePluginDir: shortTempDir(t),
		SysfsRoot:       t.TempDir(),
		RetryBackoff:    10 * time.Millisecond,
	}, logr.Discard())

	go manager.Run(context.Background())
	// Give Run a moment to enter its loop before we ask it to stop.
	time.Sleep(20 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		manager.Stop()
		manager.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not return")
	}
}

func TestManager_DistinctInstanceIDsKeepSocketsApart(t *testing.T) {
	sysfsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysfsRoot, "devices", "system", "node", "node0"), 0755); err != nil {
		t.Fatalf("failed to create node0: %v", err)
	}

	devicePluginDir := shortTempDir(t)
	fakeServer := startFakeKubelet(t, devicePluginDir)

	start := func(instanceID string, wantCalls int) *Manager {
		manager := NewManager(ManagerConfig{
			DevicePluginDir: devicePluginDir,
			SysfsRoot:       sysfsRoot,
			InstanceID:      instanceID,
			RetryBackoff:    50 * time.Millisecond,
		}, logr.Discard())
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go manager.Run(ctx)

		waitUntil(t, "registration of "+instanceID, func() bool { return len(fakeServer.recordedCalls()) >= wantCalls })
		return manager
	}

	oldManager := start("old", 1)
	newManager := start("new", 2)
	defer newManager.Stop()

	oldSocket := filepath.Join(devicePluginDir, SocketName(0, "old"))
	newSocket := filepath.Join(devicePluginDir, SocketName(0, "new"))
	for _, p := range []string{oldSocket, newSocket} {
		waitUntil(t, "socket "+p, func() bool { _, err := os.Stat(p); return err == nil })
	}

	oldManager.Stop()

	if _, err := os.Stat(newSocket); err != nil {
		t.Errorf("stopping the old manager removed the new manager's socket: %v", err)
	}
}
