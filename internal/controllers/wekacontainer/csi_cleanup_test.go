package wekacontainer

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

// Finalizing a container whose Node is already gone must still drop its tracker entry.
func TestForgetCsiTrackingWithoutNode(t *testing.T) {
	r := &containerReconcilerLoop{
		container:  &weka.WekaContainer{ObjectMeta: metav1.ObjectMeta{Name: "c-n1", Namespace: "default"}, Spec: weka.WekaContainerSpec{NodeAffinity: "n1"}},
		wekaClient: &weka.WekaClient{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default"}},
	}
	key := r.csiTrackerKey()
	csiUnreachableNodes.observe(key, "n1", epoch, true)
	t.Cleanup(func() { csiUnreachableNodes.clear(key, "n1") })

	r.forgetCsiTracking()
	if _, total := csiUnreachableNodes.share(key, epoch, time.Hour); total != 0 {
		t.Fatalf("tracker still holds %d nodes after finalization", total)
	}
}
