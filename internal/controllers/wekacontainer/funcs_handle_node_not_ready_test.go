package wekacontainer

import (
	"context"
	"errors"
	"testing"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestHandleNodeNotReadyWaitsWithoutDeletingPods(t *testing.T) {
	modes := []string{weka.WekaContainerModeDriversLoader, weka.WekaContainerModeClient}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := v1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			pod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"}, Status: v1.PodStatus{Phase: v1.PodRunning}}
			r := &containerReconcilerLoop{
				Client:        fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build(),
				ThrottlingMap: throttling.NewSyncMapThrottler(),
				container:     &weka.WekaContainer{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}, Spec: weka.WekaContainerSpec{Mode: mode}},
				node: &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"},
					Status: v1.NodeStatus{Conditions: []v1.NodeCondition{{Type: v1.NodeReady, Status: v1.ConditionFalse}}}},
				pod: pod,
			}

			err := r.HandleNodeNotReady(context.Background())

			var waitErr *lifecycle.WaitError
			if !errors.As(err, &waitErr) {
				t.Fatalf("HandleNodeNotReady = %v, want a wait error", err)
			}
			if getErr := r.Get(context.Background(), client.ObjectKeyFromObject(pod), &v1.Pod{}); getErr != nil {
				t.Fatalf("pod was deleted: %v", getErr)
			}
		})
	}
}
