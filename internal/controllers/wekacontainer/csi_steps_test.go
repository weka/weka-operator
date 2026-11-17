package wekacontainer

import (
	"context"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/services"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

func TestRecordCsiAccessibleChange(t *testing.T) {
	for _, tc := range []struct {
		name     string
		was, now bool
		want     string
	}{
		{"removed", true, false, "Warning CsiAccessibleRemoved node NotReady"},
		{"added", false, true, "Normal CsiAccessibleAdded node NotReady"},
		{"still accessible", true, true, ""},
		{"still not accessible", false, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := events.NewFakeRecorder(10)
			r := &containerReconcilerLoop{
				Recorder:  recorder,
				container: &weka.WekaContainer{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default"}},
			}
			if err := r.recordCsiAccessibleChange(tc.was, tc.now, "node NotReady"); err != nil {
				t.Fatal(err)
			}
			var got string
			select {
			case got = <-recorder.Events:
			default:
			}
			if got != tc.want {
				t.Errorf("event = %q, want %q", got, tc.want)
			}
			if len(recorder.Events) != 0 {
				t.Errorf("unexpected extra events: %d", len(recorder.Events))
			}
		})
	}
}

func TestManageCsiTopologyLabelsRecordsRemoval(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	origNs := config.Config.OperatorPodNamespace
	config.Config.OperatorPodNamespace = "default"
	t.Cleanup(func() { config.Config.OperatorPodNamespace = origNs })
	if err := services.RefreshSettings(ctx, fake.NewClientBuilder().WithScheme(scheme).Build()); err != nil {
		t.Fatal(err)
	}

	cluster := &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cl", Namespace: "default"}}
	labelKey := operations.CsiAccessibleLabelKey("cl.default.weka.io")
	// no drivers-loaded annotation: the drivers boot ID is stale, so the decision is remove
	node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{labelKey: "true"}}}
	recorder := events.NewFakeRecorder(10)
	r := &containerReconcilerLoop{
		Client:        fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build(),
		Recorder:      recorder,
		targetCluster: cluster,
		node:          node,
		container: &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default"},
			Spec:       weka.WekaContainerSpec{NodeAffinity: "n1"},
		},
	}

	if err := r.ManageCsiTopologyLabels(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.node.Labels[labelKey]; ok {
		t.Fatalf("accessible label not removed: %v", r.node.Labels)
	}
	if len(recorder.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(recorder.Events))
	}
	if got := <-recorder.Events; !strings.HasPrefix(got, "Warning CsiAccessibleRemoved ") {
		t.Errorf("event = %q", got)
	}

	if err := r.ManageCsiTopologyLabels(ctx); err != nil {
		t.Fatal(err)
	}
	if len(recorder.Events) != 0 {
		t.Errorf("unchanged label recorded %d more events", len(recorder.Events))
	}
}
