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

func TestManager_RegistersAllRegionPluginsWithKubelet(t *testing.T) {
	sysfsRoot := t.TempDir()
	nodeDir := filepath.Join(sysfsRoot, "devices", "system", "node")
	for _, name := range []string{"node0", "node1"} {
		if err := os.MkdirAll(filepath.Join(nodeDir, name), 0755); err != nil {
			t.Fatalf("failed to create %s: %v", name, err)
		}
	}

	devicePluginDir := shortTempDir(t)
	kubeletSocketPath := filepath.Join(devicePluginDir, kubeletSocketName)

	listener, err := net.Listen("unix", kubeletSocketPath)
	if err != nil {
		t.Fatalf("failed to listen on fake kubelet socket: %v", err)
	}

	fakeServer := &fakeRegistrationServer{}
	grpcServer := grpc.NewServer()
	pluginapi.RegisterRegistrationServer(grpcServer, fakeServer)
	go grpcServer.Serve(listener) //nolint:errcheck // test server, shut down via Stop() below
	defer grpcServer.Stop()

	manager := NewManager(ManagerConfig{
		DevicePluginDir: devicePluginDir,
		SysfsRoot:       sysfsRoot,
		RetryBackoff:    50 * time.Millisecond,
	}, logr.Discard())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go manager.Run(ctx)
	defer manager.Stop()

	deadline := time.After(5 * time.Second)
	for {
		if len(fakeServer.recordedCalls()) >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for registrations, got %d", len(fakeServer.recordedCalls()))
		case <-time.After(20 * time.Millisecond):
		}
	}

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
		if req.Endpoint != SocketName(region) {
			t.Errorf("region %d endpoint = %q, want %q", region, req.Endpoint, SocketName(region))
		}
		if req.Version != pluginapi.Version {
			t.Errorf("region %d version = %q, want %q", region, req.Version, pluginapi.Version)
		}
		if _, err := os.Stat(filepath.Join(devicePluginDir, SocketName(region))); err != nil {
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
