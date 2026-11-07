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
	client := &weka.WekaContainer{Spec: weka.WekaContainerSpec{Mode: weka.WekaContainerModeClient, Tolerations: []v1.Toleration{own}}}
	drive := &weka.WekaContainer{Spec: weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive, Tolerations: []v1.Toleration{own}}}
	on := services.SchedulingSettings{ClientsTolerateAll: true}

	if got := cleanupJobTolerations(client, on); !slices.Equal(got, []v1.Toleration{exists, own}) {
		t.Errorf("client with clientsTolerateAll: %v", got)
	}
	if got := cleanupJobTolerations(client, services.SchedulingSettings{}); !slices.Equal(got, []v1.Toleration{own}) {
		t.Errorf("client without the setting: %v", got)
	}
	if got := cleanupJobTolerations(drive, on); !slices.Equal(got, []v1.Toleration{own}) {
		t.Errorf("drive with clientsTolerateAll: %v", got)
	}
	if len(client.Spec.Tolerations) != 1 {
		t.Errorf("container spec mutated: %v", client.Spec.Tolerations)
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
