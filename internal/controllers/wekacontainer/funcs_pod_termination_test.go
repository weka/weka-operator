package wekacontainer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/config"
)

func mkNodeProvider(providerID string) *v1.Node {
	return &v1.Node{Spec: v1.NodeSpec{ProviderID: providerID}}
}

func mkDuration(d time.Duration) *metav1.Duration {
	return &metav1.Duration{Duration: d}
}

func TestResolveDeactivationTimeout(t *testing.T) {
	const (
		awsProvider = "aws:///eu-west-1a/i-0abc123def456"
		ociProvider = "ocid1.instance.oc1.eu-frankfurt-1.abc"
		globalNever = time.Duration(0)
	)

	tests := []struct {
		name          string
		node          *v1.Node
		override      *metav1.Duration
		globalDefault time.Duration
		want          time.Duration
	}{
		{"explicit override wins over AWS default", mkNodeProvider(awsProvider), mkDuration(5 * time.Minute), globalNever, 5 * time.Minute},
		{"explicit override 0 (never) wins even on AWS", mkNodeProvider(awsProvider), mkDuration(0), globalNever, 0},
		{"AWS node, no override -> 30m", mkNodeProvider(awsProvider), nil, globalNever, managedNodesPodTerminationTimeout},
		{"AWS default beats a non-zero global default", mkNodeProvider(awsProvider), nil, time.Hour, managedNodesPodTerminationTimeout},
		{"non-cloud node, no override -> global default (never)", mkNodeProvider(""), nil, globalNever, 0},
		{"non-cloud node uses non-zero global default", mkNodeProvider(""), nil, time.Hour, time.Hour},
		{"OCI/OKE node, no override -> 30m", mkNodeProvider(ociProvider), nil, globalNever, managedNodesPodTerminationTimeout},
		{"OCI/OKE default beats a non-zero global default", mkNodeProvider(ociProvider), nil, time.Hour, managedNodesPodTerminationTimeout},
		{"nil node, no override -> global default", nil, nil, 15 * time.Minute, 15 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveDeactivationTimeout(tt.node, tt.override, tt.globalDefault)
			if got != tt.want {
				t.Fatalf("resolveDeactivationTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func newTerminationLoop(t *testing.T, node *v1.Node) *containerReconcilerLoop {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "drive-1", Namespace: "weka"},
		Spec:       weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive},
		Status:     weka.WekaContainerStatus{Status: weka.Running},
	}
	now := metav1.NewTime(time.Now())
	return &containerReconcilerLoop{
		Client:    fake.NewClientBuilder().WithScheme(scheme).WithObjects(container).WithStatusSubresource(container).Build(),
		node:      node,
		pod:       &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "drive-1", DeletionTimestamp: &now}},
		container: container,
	}
}

// The loop has no Manager or RestClient, so any exec path would panic: a WaitError proves they were skipped.
func TestHandlePodTerminationNotReadyNodeSkipsExecKeepsStatus(t *testing.T) {
	prev := config.Config.EvictContainerOnDeletion
	config.Config.EvictContainerOnDeletion = false
	t.Cleanup(func() { config.Config.EvictContainerOnDeletion = prev })

	for name, overrides := range map[string]*weka.WekaContainerSpecOverrides{
		"default":         nil,
		"force replace":   {PodDeleteForceReplace: true},
		"upgrade replace": {UpgradeForceReplace: true},
	} {
		t.Run(name, func(t *testing.T) {
			r := newTerminationLoop(t, nodeWithReady(v1.ConditionFalse))
			r.container.Spec.Overrides = overrides
			err := r.handlePodTermination(context.Background())
			var wait *lifecycle.WaitError
			if !errors.As(err, &wait) {
				t.Fatalf("handlePodTermination() = %v, want WaitError", err)
			}
			got := &weka.WekaContainer{}
			if err := r.Get(context.Background(), client.ObjectKeyFromObject(r.container), got); err != nil {
				t.Fatal(err)
			}
			if got.Status.Status != weka.PodTerminating {
				t.Fatalf("status = %q, want %q", got.Status.Status, weka.PodTerminating)
			}
		})
	}
}

func TestHandlePodTerminationNotReadyNodeStillEvicts(t *testing.T) {
	prev := config.Config.EvictContainerOnDeletion
	config.Config.EvictContainerOnDeletion = true
	t.Cleanup(func() { config.Config.EvictContainerOnDeletion = prev })

	r := newTerminationLoop(t, nodeWithReady(v1.ConditionFalse))
	err := r.handlePodTermination(context.Background())
	var wait *lifecycle.WaitError
	if !errors.As(err, &wait) {
		t.Fatalf("handlePodTermination() = %v, want WaitError", err)
	}
	got := &weka.WekaContainer{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(r.container), got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.State != weka.ContainerStateDeleting {
		t.Fatalf("state = %q, want %q", got.Spec.State, weka.ContainerStateDeleting)
	}
}
