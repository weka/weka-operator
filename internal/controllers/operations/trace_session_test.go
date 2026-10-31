package operations

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/services"
)

type traceSessionTestManager struct {
	ctrl.Manager
	kubeClient client.Client
	scheme     *runtime.Scheme
}

func (m *traceSessionTestManager) GetClient() client.Client {
	return m.kubeClient
}

func (m *traceSessionTestManager) GetScheme() *runtime.Scheme {
	return m.scheme
}

func TestTraceSessionDeploymentPropagatesServiceAccount(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("add WEKA types to scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add apps types to scheme: %v", err)
	}

	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	mgr := &traceSessionTestManager{
		kubeClient: kubeClient,
		scheme:     scheme,
	}
	policy := &weka.WekaPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "trace-policy",
			Namespace: "default",
			UID:       types.UID("trace-policy-uid"),
		},
	}
	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "weka-cluster",
			Namespace: "default",
			UID:       types.UID("weka-cluster-uid"),
		},
	}
	serviceAccountName := "weka-runtime"
	op := &MaintainTraceSession{
		cluster:  cluster,
		payload:  &weka.RemoteTracesSessionConfig{},
		mgr:      mgr,
		ownerRef: policy,
		containerDetails: weka.WekaOwnerDetails{
			Image:              "taskmon:latest",
			ImagePullSecret:    "weka-registry",
			ServiceAccountName: serviceAccountName,
		},
	}

	if err := op.EnsureDeployment(context.Background()); err != nil {
		t.Fatalf("EnsureDeployment() error = %v", err)
	}

	var deployment appsv1.Deployment
	key := client.ObjectKey{Name: "trace-session-" + policy.Name, Namespace: policy.Namespace}
	if err := kubeClient.Get(context.Background(), key, &deployment); err != nil {
		t.Fatalf("get trace session deployment: %v", err)
	}
	if got := deployment.Spec.Template.Spec.ServiceAccountName; got != serviceAccountName {
		t.Errorf("ServiceAccountName = %q, want %q", got, serviceAccountName)
	}
}

// fakeTraceWekaService embeds the interface so only the two calls the routing makes need bodies.
type fakeTraceWekaService struct {
	services.WekaService
	processes          string
	containers         string
	listContainerCalls int
}

func (f *fakeTraceWekaService) ListProcesses(_ context.Context, _ services.ProcessListOptions) ([]services.Process, error) {
	var out []services.Process
	return out, json.Unmarshal([]byte(f.processes), &out)
}

func (f *fakeTraceWekaService) ListClusterContainers(_ context.Context) ([]services.WekaClusterContainer, error) {
	f.listContainerCalls++
	var out []services.WekaClusterContainer
	return out, json.Unmarshal([]byte(f.containers), &out)
}

func TestBuildTraceRouting(t *testing.T) {
	tests := []struct {
		name               string
		processes          string
		containers         string
		want               TraceServerDiscovery
		wantContainerCalls int
	}{
		{
			name: "6.x endpoints come from one container list, clients included, no-ip container skipped",
			processes: `[
				{"process": 0, "container": 0, "container_name": "drivex0", "slot": 0},
				{"process": 1, "container": 0, "container_name": "drivex0", "slot": 1},
				{"process": 2, "container": 1, "container_name": "client", "slot": 1},
				{"process": 3, "container": 2, "container_name": "gone", "slot": 0}
			]`,
			containers: `[
				{"id": 0, "name": "drivex0", "ips": ["10.0.0.1"], "port": 15000},
				{"id": 1, "name": "client", "ips": ["fd00::2"], "port": 14000},
				{"id": 2, "name": "gone", "ips": [], "port": 15100}
			]`,
			want: TraceServerDiscovery{
				Nodes: map[int]NodeInfo{
					0: {Slot: 0, ContainerID: 0},
					1: {Slot: 1, ContainerID: 0},
					2: {Slot: 1, ContainerID: 1},
					3: {Slot: 0, ContainerID: 2},
				},
				Containers: map[int]ContainerInfo{
					0: {Name: "drivex0", TraceServerEndpoint: "10.0.0.1:15050"},
					1: {Name: "client", TraceServerEndpoint: "[fd00::2]:14050"},
				},
			},
			wantContainerCalls: 1,
		},
		{
			name: "5.x reads ids, slot and endpoint from the process list only",
			processes: `[
				{"node_id": "NodeId<0>", "host_id": "HostId<0>", "container_name": "drivex0", "slot": 0, "mgmt_ips": ["10.0.0.1"], "mgmt_port": 15000,
				 "nodeInfo": {"host_id": "HostId<0>", "container_name": "drivex0", "mgmt_ips": ["10.0.0.1"], "mgmt_port": 15000}},
				{"node_id": "NodeId<1>", "host_id": "HostId<0>", "container_name": "drivex0", "slot": 1, "mgmt_ips": ["10.0.0.1"], "mgmt_port": 15000,
				 "nodeInfo": {"host_id": "HostId<0>", "container_name": "drivex0", "mgmt_ips": ["10.0.0.1"], "mgmt_port": 15000}}
			]`,
			want: TraceServerDiscovery{
				Nodes: map[int]NodeInfo{
					0: {Slot: 0, ContainerID: 0},
					1: {Slot: 1, ContainerID: 0},
				},
				Containers: map[int]ContainerInfo{
					0: {Name: "drivex0", TraceServerEndpoint: "10.0.0.1:15050"},
				},
			},
			wantContainerCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeTraceWekaService{processes: tt.processes, containers: tt.containers}
			got, err := buildTraceRouting(context.Background(), svc)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(*got, tt.want) {
				t.Errorf("routing = %+v, want %+v", *got, tt.want)
			}
			if svc.listContainerCalls != tt.wantContainerCalls {
				t.Errorf("ListClusterContainers called %d times, want %d", svc.listContainerCalls, tt.wantContainerCalls)
			}
		})
	}
}
