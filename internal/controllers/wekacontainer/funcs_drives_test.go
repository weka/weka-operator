package wekacontainer

import (
	"context"
	"strconv"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/tools/record"

	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
)

func TestCheckDriveResourceFeasibility(t *testing.T) {
	// Pin the per-core model so required cores/hugepages/memory are deterministic regardless of env.
	globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = 5120
	globalconfig.Config.ClusterCapacity.QlcCapacityPerCoreGiB = 51200
	globalconfig.Config.ClusterCapacity.ImbalanceFactor = 2.0

	// 10240 GiB TLC => reqCores=2, reqHugepages=3200 MiB, reqMemory=8000+2*3000=14000 MiB.
	const tlcGiB = 10240

	// drivePod reflects what the RUNNING pod reserves: cores and hugepages in their requests, RSS in the
	// memory request.
	drivePod := func(cpuCores int, hpReq, memReq string) *v1.Pod {
		return &v1.Pod{Spec: v1.PodSpec{Containers: []v1.Container{{
			Name: consts.WekaContainerName,
			Resources: v1.ResourceRequirements{Requests: v1.ResourceList{
				v1.ResourceCPU: resource.MustParse(strconv.Itoa(cpuCores)),
				v1.ResourceName(string(v1.ResourceHugePagesPrefix) + "2Mi"): resource.MustParse(hpReq),
				v1.ResourceMemory: resource.MustParse(memReq),
			}},
		}}}}
	}
	sharingContainer := func() *weka.WekaContainer {
		return &weka.WekaContainer{
			Spec: weka.WekaContainerSpec{ContainerCapacity: tlcGiB, NumCores: 2},
			Status: weka.WekaContainerStatus{Allocations: &weka.ContainerAllocations{
				VirtualDrives: []weka.VirtualDrive{{CapacityGiB: tlcGiB, Type: "TLC"}},
			}},
		}
	}

	tests := []struct {
		name      string
		container *weka.WekaContainer
		pod       *v1.Pod
		wantErr   bool
	}{
		{
			name:      "sized for capacity passes",
			container: sharingContainer(),
			pod:       drivePod(3, "3200Mi", "14000Mi"),
			wantErr:   false,
		},
		{
			name:      "cores shortfall blocks",
			container: sharingContainer(),
			pod:       drivePod(1, "3200Mi", "14000Mi"),
			wantErr:   true,
		},
		{
			name:      "hugepages shortfall blocks",
			container: sharingContainer(),
			pod:       drivePod(3, "1000Mi", "14000Mi"),
			wantErr:   true,
		},
		{
			name:      "memory shortfall blocks",
			container: sharingContainer(),
			pod:       drivePod(3, "3200Mi", "10000Mi"),
			wantErr:   true,
		},
		{
			// A sidecar injected at index 0 must not be mistaken for the weka container: the under-resourced
			// weka container (index 1) must still be detected and block the add.
			name:      "weka container found by name, not index 0",
			container: sharingContainer(),
			pod: func() *v1.Pod {
				p := drivePod(1, "3200Mi", "14000Mi") // weka container: 1 core (short)
				sidecar := v1.Container{Name: "istio-proxy", Resources: v1.ResourceRequirements{Requests: v1.ResourceList{
					v1.ResourceCPU: resource.MustParse("8"), // well-resourced sidecar that would mask the shortfall
				}}}
				p.Spec.Containers = append([]v1.Container{sidecar}, p.Spec.Containers...)
				return p
			}(),
			wantErr: true,
		},
		{
			name: "non-drive-sharing container skipped",
			container: &weka.WekaContainer{
				Spec:   weka.WekaContainerSpec{NumDrives: 4},
				Status: weka.WekaContainerStatus{Allocations: &weka.ContainerAllocations{}},
			},
			pod:     drivePod(3, "3200Mi", "14000Mi"),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &containerReconcilerLoop{
				container: tt.container,
				pod:       tt.pod,
				Recorder:  record.NewFakeRecorder(10),
			}
			err := r.checkDriveResourceFeasibility(context.Background())
			if tt.wantErr && err == nil {
				t.Fatalf("expected a feasibility error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

// TestNeedsDrivesToAllocate covers the enableDynamicDriveScalingForSharedDrives=false gate: when the
// flag is off it must still allow ADDING a brand-new pool/type to a drive-sharing container, while
// refusing to GROW an already-allocated type in place.
func TestNeedsDrivesToAllocate(t *testing.T) {
	// Save/restore the global flag so the test is order-independent.
	saved := globalconfig.Config.DriveSharing.EnableDynamicDriveScaling
	defer func() { globalconfig.Config.DriveSharing.EnableDynamicDriveScaling = saved }()

	// 1000 GiB with a 1:1 ratio splits into desiredTlc=500, desiredQlc=500.
	const capGiB = 1000
	ratio := &weka.DriveTypesRatio{Tlc: 1, Qlc: 1}

	tests := []struct {
		name       string
		flagOn     bool
		container  *weka.WekaContainer
		wantResult bool
	}{
		{
			// Flag off: a container holding only a QLC vdrive is now asked for a TLC pool too
			// (desiredTlc>0, curTlc==0). Adding a new type is allowed.
			name:   "flag off: adding a new TLC pool to a QLC-only container is allowed",
			flagOn: false,
			container: &weka.WekaContainer{
				Spec: weka.WekaContainerSpec{ContainerCapacity: capGiB, DriveTypesRatio: ratio},
				Status: weka.WekaContainerStatus{Allocations: &weka.ContainerAllocations{
					VirtualDrives: []weka.VirtualDrive{{CapacityGiB: 500, Type: "QLC"}},
				}},
			},
			wantResult: true,
		},
		{
			// Flag off: the TLC type already exists but is under desired (curTlc>0). Growing an
			// existing type in place is forbidden.
			name:   "flag off: growing an existing TLC type in place is blocked",
			flagOn: false,
			container: &weka.WekaContainer{
				Spec: weka.WekaContainerSpec{ContainerCapacity: capGiB, DriveTypesRatio: ratio},
				Status: weka.WekaContainerStatus{Allocations: &weka.ContainerAllocations{
					VirtualDrives: []weka.VirtualDrive{
						{CapacityGiB: 100, Type: "TLC"}, // curTlc>0 but < desiredTlc=500
						{CapacityGiB: 500, Type: "QLC"},
					},
				}},
			},
			wantResult: false,
		},
		{
			// Flag off: numDrives-mode drive-sharing (DriveCapacity>0, no ContainerCapacity) has no
			// per-type notion, so it stays blocked.
			name:   "flag off: numDrives-mode drive-sharing container stays blocked",
			flagOn: false,
			container: &weka.WekaContainer{
				Spec: weka.WekaContainerSpec{DriveCapacity: 100, NumDrives: 4},
				Status: weka.WekaContainerStatus{Allocations: &weka.ContainerAllocations{
					VirtualDrives: []weka.VirtualDrive{{CapacityGiB: 100, Type: "TLC"}},
				}},
			},
			wantResult: false,
		},
		{
			// Flag on: existing behavior preserved — a capacity shortfall triggers allocation.
			name:   "flag on: capacity shortfall triggers allocation",
			flagOn: true,
			container: &weka.WekaContainer{
				Spec: weka.WekaContainerSpec{ContainerCapacity: capGiB, DriveTypesRatio: ratio},
				Status: weka.WekaContainerStatus{Allocations: &weka.ContainerAllocations{
					VirtualDrives: []weka.VirtualDrive{{CapacityGiB: 300, Type: "TLC"}},
				}},
			},
			wantResult: true,
		},
		{
			// Nil allocations short-circuits to false regardless of the flag (covered by cluster's
			// AllocateResources).
			name:   "flag off: nil allocations returns false",
			flagOn: false,
			container: &weka.WekaContainer{
				Spec:   weka.WekaContainerSpec{ContainerCapacity: capGiB, DriveTypesRatio: ratio},
				Status: weka.WekaContainerStatus{Allocations: nil},
			},
			wantResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			globalconfig.Config.DriveSharing.EnableDynamicDriveScaling = tt.flagOn
			r := &containerReconcilerLoop{container: tt.container}
			if got := r.NeedsDrivesToAllocate(); got != tt.wantResult {
				t.Fatalf("NeedsDrivesToAllocate() = %v, want %v", got, tt.wantResult)
			}
		})
	}
}
