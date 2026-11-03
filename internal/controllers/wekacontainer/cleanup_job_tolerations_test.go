package wekacontainer

import (
	"context"
	"slices"
	"testing"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/services"
)

func TestCleanupJobTolerations(t *testing.T) {
	exists := v1.Toleration{Operator: v1.TolerationOpExists}
	own := v1.Toleration{Key: "gpu", Operator: v1.TolerationOpExists}
	secs := int64(30)
	finite := v1.Toleration{Key: "example.com/hold", Operator: v1.TolerationOpExists, Effect: v1.TaintEffectNoExecute, TolerationSeconds: &secs}

	tests := []struct {
		name string
		mode string
		own  []v1.Toleration
		want []v1.Toleration
	}{
		{"client", weka.WekaContainerModeClient, []v1.Toleration{own}, []v1.Toleration{exists, own}},
		{"drive", weka.WekaContainerModeDrive, []v1.Toleration{own}, []v1.Toleration{exists, own}},
		{"no own tolerations", weka.WekaContainerModeCompute, nil, []v1.Toleration{exists}},
		{"a finite NoExecute toleration comes after the catch-all", weka.WekaContainerModeClient, []v1.Toleration{finite}, []v1.Toleration{exists, finite}},
		{"a catch-all of the container's is not repeated", weka.WekaContainerModeClient, []v1.Toleration{exists, own}, []v1.Toleration{exists, own}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			container := &weka.WekaContainer{Spec: weka.WekaContainerSpec{Mode: tt.mode, Tolerations: slices.Clone(tt.own)}}
			if got := cleanupJobTolerations(container); !slices.Equal(got, tt.want) {
				t.Errorf("tolerations = %v, want %v", got, tt.want)
			}
			if !slices.Equal(container.Spec.Tolerations, tt.own) {
				t.Errorf("container spec mutated: %v", container.Spec.Tolerations)
			}
		})
	}
}

func TestUpdatePodTolerationsAddsExistsUnderClientsTolerateAll(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	exists := v1.Toleration{Operator: v1.TolerationOpExists}
	for _, tolerateAll := range []bool{true, false} {
		pod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"}}
		r := &containerReconcilerLoop{
			Client:     fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build(),
			pod:        pod,
			container:  &weka.WekaContainer{Spec: weka.WekaContainerSpec{Mode: weka.WekaContainerModeClient}},
			scheduling: services.SchedulingSettings{ClientsTolerateAll: tolerateAll},
		}
		if err := r.updatePodTolerationsOnChange(context.Background()); err != nil {
			t.Fatal(err)
		}
		got := &v1.Pod{}
		if err := r.Get(context.Background(), client.ObjectKeyFromObject(pod), got); err != nil {
			t.Fatal(err)
		}
		if has := slices.Contains(got.Spec.Tolerations, exists); has != tolerateAll {
			t.Errorf("clientsTolerateAll=%v: pod has Exists = %v", tolerateAll, has)
		}
	}
}
