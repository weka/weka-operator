package wekacontainer

import (
	"context"
	"strconv"
	"testing"

	"github.com/weka/go-steps-engine/throttling"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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

	fullDrivesContainer := func(allocated int) *weka.WekaContainer {
		c := &weka.WekaContainer{Spec: weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive, NumDrives: 3, Hugepages: 1000}}
		c.Status.Allocations = &weka.ContainerAllocations{Drives: []string{"a", "b", "c"}[:allocated]}
		return c
	}
	fullDrivesPod := func(drives int, hpReq string) *v1.Pod {
		return &v1.Pod{Spec: v1.PodSpec{Containers: []v1.Container{{
			Name: consts.WekaContainerName,
			Resources: v1.ResourceRequirements{Requests: v1.ResourceList{
				consts.ResourceDrives: resource.MustParse(strconv.Itoa(drives)),
				v1.ResourceName(string(v1.ResourceHugePagesPrefix) + "2Mi"): resource.MustParse(hpReq),
			}},
		}}}}
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
				p := drivePod(3, "1000Mi", "14000Mi") // weka container: hugepages short (1000 < 3200)
				sidecar := v1.Container{Name: "istio-proxy", Resources: v1.ResourceRequirements{Requests: v1.ResourceList{
					v1.ResourceName(string(v1.ResourceHugePagesPrefix) + "2Mi"): resource.MustParse("8000Mi"), // would mask the shortfall
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
		{
			name:      "full drives: pod requests fewer drives than allocated blocks",
			container: fullDrivesContainer(3),
			pod:       fullDrivesPod(2, "1000Mi"),
			wantErr:   true,
		},
		{
			name:      "full drives: pod covers the allocated drives passes",
			container: fullDrivesContainer(3),
			pod:       fullDrivesPod(3, "1000Mi"),
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &containerReconcilerLoop{
				container: tt.container,
				pod:       tt.pod,
				Recorder:  events.NewFakeRecorder(10),
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

func TestHandleOversizedDrivePod(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		requested   int
		nodeName    string
		wantDeleted bool
		wantEvent   bool
	}{
		{name: "request within numDrives: nothing", requested: 2},
		{name: "unscheduled pod is deleted", requested: 4, wantDeleted: true},
		{name: "scheduled pod only warns", requested: 4, nodeName: "n1", wantEvent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default"},
				Spec: v1.PodSpec{NodeName: tc.nodeName, Containers: []v1.Container{{
					Name: consts.WekaContainerName,
					Resources: v1.ResourceRequirements{Requests: v1.ResourceList{
						consts.ResourceDrives: resource.MustParse(strconv.Itoa(tc.requested)),
					}},
				}}},
			}
			recorder := events.NewFakeRecorder(10)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
			r := &containerReconcilerLoop{
				Client:        c,
				Recorder:      recorder,
				ThrottlingMap: throttling.NewSyncMapThrottler(),
				container: &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default"},
					Spec:       weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive, NumDrives: 2},
				},
				pod: pod,
			}
			err := r.handleOversizedDrivePod(context.Background())
			if tc.wantDeleted == (err == nil) {
				t.Errorf("err = %v, want a wait error only when the pod is deleted", err)
			}
			getErr := c.Get(context.Background(), client.ObjectKeyFromObject(pod), &v1.Pod{})
			if gone := apierrors.IsNotFound(getErr); gone != tc.wantDeleted {
				t.Errorf("pod deleted = %v, want %v (get err: %v)", gone, tc.wantDeleted, getErr)
			}
			if got := len(recorder.Events) > 0; got != tc.wantEvent {
				t.Errorf("warning event = %v, want %v", got, tc.wantEvent)
			}
		})
	}
}
