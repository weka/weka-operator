package allocator

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zerologr"
	"github.com/rs/zerolog"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func setupLogging(ctx context.Context) (context.Context, func(context.Context) error, error) {
	var logger logr.Logger
	if os.Getenv("DEBUG") == "true" {
		// Debug logger
		writer := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.TimeOnly}
		zeroLogger := zerolog.New(writer).Level(zerolog.DebugLevel).With().Timestamp().Logger()
		logger = zerologr.New(&zeroLogger)
	} else {
		// Logger that drops/silences messages for unit testing
		zeroLogger := zerolog.Nop()
		logger = zerologr.New(&zeroLogger)
	}

	shutdown, err := instrumentation.SetupOTelSDK(ctx, "test-weka-operator", "", logger)
	if err != nil {
		return nil, nil, fmt.Errorf("Failed to set up OTel SDK: %v", err)
	}

	ctx, _ = instrumentation.GetLoggerForContext(ctx, &logger, "")
	return ctx, shutdown, nil
}

// TestPortAllocationWithGlobalRanges tests that port allocation correctly avoids global singleton ports
// This is a core test for the fix we made to prevent container agent ports from conflicting with
// global singleton ports (LB, S3, LbAdmin) which all use the same offset range.
func TestPortAllocationWithGlobalRanges(t *testing.T) {
	tests := []struct {
		name            string
		clusterRange    Range
		globalRanges    []Range // Global singleton ports (LB, S3, etc.)
		nodePortClaims  []Range // Existing per-container port claims
		requestedSize   int
		requestedOffset int
		expectedBase    int
		expectError     bool
	}{
		{
			name:         "allocate agent port avoiding global singleton ports",
			clusterRange: Range{Base: 35000, Size: 500},
			globalRanges: []Range{
				{Base: 35300, Size: 1}, // LB port
				{Base: 35301, Size: 1}, // LbAdmin port
				{Base: 35302, Size: 1}, // S3 port
			},
			nodePortClaims:  []Range{},
			requestedSize:   1,
			requestedOffset: SinglePortsOffset, // 300
			expectedBase:    35303,             // Should skip the 3 global singleton ports
			expectError:     false,
		},
		{
			name:         "allocate weka port (100 ports from base)",
			clusterRange: Range{Base: 35000, Size: 500},
			globalRanges: []Range{
				{Base: 35300, Size: 1},
				{Base: 35301, Size: 1},
				{Base: 35302, Size: 1},
			},
			nodePortClaims:  []Range{},
			requestedSize:   100,
			requestedOffset: 0,
			expectedBase:    35000, // Should get base port
			expectError:     false,
		},
		{
			name:         "allocate agent port avoiding both global and node claims",
			clusterRange: Range{Base: 35000, Size: 500},
			globalRanges: []Range{
				{Base: 35300, Size: 1}, // LB
				{Base: 35301, Size: 1}, // LbAdmin
				{Base: 35302, Size: 1}, // S3
			},
			nodePortClaims: []Range{
				{Base: 35303, Size: 1}, // Existing container agent port
			},
			requestedSize:   1,
			requestedOffset: SinglePortsOffset,
			expectedBase:    35304, // Should skip both global ports and existing claim
			expectError:     false,
		},
		{
			name:         "allocate weka port avoiding existing node claim",
			clusterRange: Range{Base: 35000, Size: 500},
			globalRanges: []Range{},
			nodePortClaims: []Range{
				{Base: 35000, Size: 100}, // Existing weka port range
			},
			requestedSize:   100,
			requestedOffset: 0,
			expectedBase:    35100, // Should skip to next available range
			expectError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Combine global ranges and node port claims
			allAllocatedRanges := append(tt.globalRanges, tt.nodePortClaims...)

			// Call GetFreeRangeWithOffset
			result, err := GetFreeRangeWithOffset(tt.clusterRange, allAllocatedRanges, tt.requestedSize, tt.requestedOffset)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if result != tt.expectedBase {
				t.Errorf("Expected base port %d, got %d", tt.expectedBase, result)
			}
		})
	}
}

// TestGetAvailableDrives tests drive filtering logic
func TestGetAvailableDrives(t *testing.T) {
	ctx, _, err := setupLogging(context.Background())
	if err != nil {
		t.Fatalf("Failed to setup logging: %v", err)
	}

	tests := []struct {
		name           string
		node           *v1.Node
		claims         *NodeClaims
		expectedDrives []string
		expectError    bool
	}{
		{
			name: "all drives available",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						"weka.io/weka-drives": `["drive1", "drive2", "drive3"]`,
					},
				},
			},
			claims:         NewNodeClaims(),
			expectedDrives: []string{"drive1", "drive2", "drive3"},
			expectError:    false,
		},
		{
			name: "some drives claimed",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						"weka.io/weka-drives": `["drive1", "drive2", "drive3"]`,
					},
				},
			},
			claims: &NodeClaims{
				Drives: map[string]ClaimKey{
					"drive1": "cluster1:default:container1",
				},
				Ports: make(map[string]ClaimKey),
			},
			expectedDrives: []string{"drive2", "drive3"},
			expectError:    false,
		},
		{
			name: "all drives claimed",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Annotations: map[string]string{
						"weka.io/weka-drives": `["drive1", "drive2"]`,
					},
				},
			},
			claims: &NodeClaims{
				Drives: map[string]ClaimKey{
					"drive1": "cluster1:default:container1",
					"drive2": "cluster1:default:container2",
				},
				Ports: make(map[string]ClaimKey),
			},
			expectedDrives: []string{},
			expectError:    false,
		},
		{
			name: "no drives annotation",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "node1",
					Annotations: map[string]string{},
				},
			},
			claims:         NewNodeClaims(),
			expectedDrives: []string{},
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client with the node
			scheme := runtime.NewScheme()
			_ = v1.AddToScheme(scheme)
			_ = weka.AddToScheme(scheme)
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.node).
				Build()

			allocator := &ContainerResourceAllocator{
				client: fakeClient,
			}

			drives, err := allocator.GetAvailableDrives(ctx, tt.node, tt.claims)

			if tt.expectError && err == nil {
				t.Error("Expected error, got nil")
				return
			}

			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if len(drives) != len(tt.expectedDrives) {
				t.Errorf("Expected %d drives, got %d", len(tt.expectedDrives), len(drives))
				return
			}

			// Check all expected drives are present
			driveMap := make(map[string]bool)
			for _, d := range drives {
				driveMap[d] = true
			}

			for _, expected := range tt.expectedDrives {
				if !driveMap[expected] {
					t.Errorf("Expected drive %q not found in result", expected)
				}
			}
		})
	}
}

// TestEnsureGlobalRangeWithNodeClaims tests that global range allocation considers per-container claims
func TestEnsureGlobalRangeWithNodeClaims(t *testing.T) {
	owner := OwnerCluster{
		ClusterName: "test-cluster",
		Namespace:   "default",
	}

	tests := []struct {
		name           string
		allocations    *Allocations
		nodePortClaims []Range
		rangeName      string
		rangeSize      int
		rangeOffset    int
		expectedBase   int
		expectError    bool
	}{
		{
			name: "allocate LB port avoiding container agent port",
			allocations: &Allocations{
				Global: GlobalAllocations{
					ClusterRanges: map[OwnerCluster]Range{
						owner: {Base: 35000, Size: 500},
					},
					AllocatedRanges: map[OwnerCluster]map[string]Range{},
				},
			},
			nodePortClaims: []Range{
				{Base: 35300, Size: 1}, // Existing container agent port at offset
			},
			rangeName:    "lb",
			rangeSize:    1,
			rangeOffset:  SinglePortsOffset,
			expectedBase: 35301, // Should skip the existing agent port
			expectError:  false,
		},
		{
			name: "allocate S3 port with existing LB and LbAdmin",
			allocations: &Allocations{
				Global: GlobalAllocations{
					ClusterRanges: map[OwnerCluster]Range{
						owner: {Base: 35000, Size: 500},
					},
					AllocatedRanges: map[OwnerCluster]map[string]Range{
						owner: {
							"lb":      {Base: 35300, Size: 1},
							"lbAdmin": {Base: 35301, Size: 1},
						},
					},
				},
			},
			nodePortClaims: []Range{},
			rangeName:      "s3",
			rangeSize:      1,
			rangeOffset:    SinglePortsOffset,
			expectedBase:   35302, // Should get next port after LB and LbAdmin
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.allocations.EnsureGlobalRangeWithOffset(
				owner,
				tt.rangeName,
				tt.rangeSize,
				tt.rangeOffset,
				tt.nodePortClaims,
			)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if result.Base != tt.expectedBase {
				t.Errorf("Expected base %d, got %d", tt.expectedBase, result.Base)
			}

			// Verify it was added to allocations
			if allocated, ok := tt.allocations.Global.AllocatedRanges[owner][tt.rangeName]; !ok {
				t.Error("Range was not added to allocations")
			} else if allocated.Base != tt.expectedBase {
				t.Errorf("Allocated range has wrong base: expected %d, got %d", tt.expectedBase, allocated.Base)
			}
		})
	}
}
