package resources

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

func TestWaitAndLoad_AbortsOnShutdown(t *testing.T) {
	orig := retryInterval
	retryInterval = time.Millisecond
	defer func() { retryInterval = orig }()

	// resourcesPath won't exist in the test environment, so phase 1 loops,
	// sleeps retryInterval, then shouldAbort returns true → aborts with error.
	_, err := WaitAndLoad(context.Background(), func() bool { return true })
	if err == nil {
		t.Fatal("expected error when shutdown requested, got nil")
	}
	if !strings.Contains(err.Error(), "shutdown") {
		t.Errorf("error = %q, want it to mention shutdown", err.Error())
	}
}

func TestWaitAndLoad_CtxCancel(t *testing.T) {
	orig := retryInterval
	retryInterval = time.Millisecond
	defer func() { retryInterval = orig }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancelled

	_, err := WaitAndLoad(ctx, func() bool { return false })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// TestNodeResourcesIsContainerAllocations pins the producer/consumer contract: the
// operator marshals weka.ContainerAllocations and the runtime must decode the exact
// same wire shape via NodeResources, with no field drift or tag fork.
func TestNodeResourcesIsContainerAllocations(t *testing.T) {
	fd := "fd-1"
	produced := weka.ContainerAllocations{
		Drives:            []string{"drive-1", "drive-2"},
		WekaPort:          14000,
		AgentPort:         15000,
		FailureDomain:     &fd,
		MachineIdentifier: "machine-1",
		NetDevices:        []string{"eth0", "eth1"},
	}
	data, err := json.Marshal(produced)
	if err != nil {
		t.Fatal(err)
	}

	var consumed NodeResources
	if err := json.Unmarshal(data, &consumed); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(consumed, produced) {
		t.Fatalf("round trip mismatch: got %+v, want %+v", consumed, produced)
	}
}
