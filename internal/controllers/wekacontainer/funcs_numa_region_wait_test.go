package wekacontainer

import (
	"errors"
	"testing"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

func TestWaitForNumaRegionResource(t *testing.T) {
	region := 1
	const res = v1.ResourceName("weka.io/numa-region-1")
	numa := func(method weka.WekaNumaMethod) *weka.WekaNuma {
		return &weka.WekaNuma{Single: true, Region: &region, Method: method}
	}
	node := func(alloc v1.ResourceList) *v1.Node {
		return &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Status: v1.NodeStatus{
			Allocatable: alloc,
			Conditions:  []v1.NodeCondition{{Type: v1.NodeReady, Status: v1.ConditionTrue}},
		}}
	}
	notReady := node(v1.ResourceList{res: resource.MustParse("1")})
	notReady.Status.Conditions[0].Status = v1.ConditionFalse

	tests := []struct {
		name     string
		numa     *weka.WekaNuma
		node     *v1.Node
		wantWait bool
	}{
		{"absent", numa(""), node(nil), true},
		{"zero", numa(""), node(v1.ResourceList{res: resource.MustParse("0")}), true},
		{"other region only", numa(""), node(v1.ResourceList{"weka.io/numa-region-0": resource.MustParse("1")}), true},
		{"advertised", numa(""), node(v1.ResourceList{res: resource.MustParse("1")}), false},
		{"explicit device-plugin, advertised", numa(weka.WekaNumaMethodDevicePlugin), node(v1.ResourceList{res: resource.MustParse("1")}), false},
		{"not ready with stale allocatable", numa(""), notReady, true},
		{"no numa", nil, node(nil), false},
		{"not single", &weka.WekaNuma{Region: &region}, node(nil), false},
		{"dra does not request the resource", numa(weka.WekaNumaMethodDra), node(nil), false},
		{"node unknown", numa(""), nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &containerReconcilerLoop{
				ThrottlingMap: throttling.NewSyncMapThrottler(),
				Recorder:      events.NewFakeRecorder(10),
				container: &weka.WekaContainer{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
					Spec: weka.WekaContainerSpec{Mode: weka.WekaContainerModeClient, Numa: tt.numa}},
			}
			err := r.waitForNumaRegionResource(tt.node)
			var waitErr *lifecycle.WaitError
			if got := errors.As(err, &waitErr); got != tt.wantWait {
				t.Fatalf("err = %v, want wait error: %v", err, tt.wantWait)
			}
			if err != nil && !tt.wantWait {
				t.Fatalf("unexpected error %v", err)
			}
		})
	}
}
