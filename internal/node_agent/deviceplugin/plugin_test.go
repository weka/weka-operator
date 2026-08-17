package deviceplugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

func TestPlugin_ServeAllocateListAndWatch(t *testing.T) {
	dir := shortTempDir(t)
	socketPath := filepath.Join(dir, SocketName(2))

	plugin := NewPlugin(2, socketPath, logr.Discard())
	if err := plugin.Serve(); err != nil {
		t.Fatalf("Serve() error: %v", err)
	}
	defer plugin.Stop()

	conn, err := grpc.NewClient("unix://"+socketPath, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test cleanup

	client := pluginapi.NewDevicePluginClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	opts, err := client.GetDevicePluginOptions(ctx, &pluginapi.Empty{})
	if err != nil {
		t.Fatalf("GetDevicePluginOptions error: %v", err)
	}
	if opts.PreStartRequired || opts.GetPreferredAllocationAvailable {
		t.Errorf("unexpected device plugin options: %+v", opts)
	}

	stream, err := client.ListAndWatch(ctx, &pluginapi.Empty{})
	if err != nil {
		t.Fatalf("ListAndWatch error: %v", err)
	}
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("ListAndWatch Recv error: %v", err)
	}
	if len(resp.Devices) != DevicesPerRegion {
		t.Fatalf("got %d devices, want %d", len(resp.Devices), DevicesPerRegion)
	}
	for _, d := range resp.Devices {
		if d.Health != pluginapi.Healthy {
			t.Errorf("device %s health = %s, want Healthy", d.ID, d.Health)
		}
	}

	allocResp, err := client.Allocate(ctx, &pluginapi.AllocateRequest{
		ContainerRequests: []*pluginapi.ContainerAllocateRequest{
			{DevicesIDs: []string{DeviceID(2, 0)}},
		},
	})
	if err != nil {
		t.Fatalf("Allocate error: %v", err)
	}
	if len(allocResp.ContainerResponses) != 1 {
		t.Fatalf("got %d container responses, want 1", len(allocResp.ContainerResponses))
	}
	if got, want := allocResp.ContainerResponses[0].Envs[WekaNumaRegionEnv], "2"; got != want {
		t.Errorf("%s env = %q, want %q", WekaNumaRegionEnv, got, want)
	}

	if _, err := client.GetPreferredAllocation(ctx, &pluginapi.PreferredAllocationRequest{}); err != nil {
		t.Errorf("GetPreferredAllocation error: %v", err)
	}
	if _, err := client.PreStartContainer(ctx, &pluginapi.PreStartContainerRequest{}); err != nil {
		t.Errorf("PreStartContainer error: %v", err)
	}
}

func TestPlugin_StopRemovesSocket(t *testing.T) {
	dir := shortTempDir(t)
	socketPath := filepath.Join(dir, SocketName(0))

	plugin := NewPlugin(0, socketPath, logr.Discard())
	if err := plugin.Serve(); err != nil {
		t.Fatalf("Serve() error: %v", err)
	}

	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("expected socket to exist after Serve(): %v", err)
	}

	plugin.Stop()

	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Errorf("expected socket to be removed after Stop(), stat error: %v", err)
	}
}

func TestPlugin_ServeRemovesStaleSocket(t *testing.T) {
	dir := shortTempDir(t)
	socketPath := filepath.Join(dir, SocketName(1))

	// Simulate a stale socket file left behind by a previous, uncleanly-stopped run.
	if err := os.WriteFile(socketPath, []byte{}, 0644); err != nil {
		t.Fatalf("failed to create stale socket file: %v", err)
	}

	plugin := NewPlugin(1, socketPath, logr.Discard())
	if err := plugin.Serve(); err != nil {
		t.Fatalf("Serve() should recover from a stale socket file, got error: %v", err)
	}
	defer plugin.Stop()
}
