package validation

import (
	"context"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// cacertFakeClient seeds a fake client with the given WekaCluster/WekaClient objects.
func cacertFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(weka): %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func TestClientWekahomeCacertNotInherited(t *testing.T) {
	crossCluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "cluster-ns"},
		Spec:       weka.WekaClusterSpec{WekaHome: &weka.WekaHomeConfig{CacertSecret: "cluster-secret"}},
	}
	sameNsCluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "client-ns"},
		Spec:       weka.WekaClusterSpec{WekaHome: &weka.WekaHomeConfig{CacertSecret: "cluster-secret"}},
	}

	tests := []struct {
		name       string
		obj        runtime.Object
		seed       []client.Object
		wantErrs   int
		wantDetail string
	}{
		{
			name: "cross-namespace cluster with cacertSecret",
			obj: &weka.WekaClient{
				ObjectMeta: metav1.ObjectMeta{Namespace: "client-ns"},
				Spec:       weka.WekaClientSpec{TargetCluster: weka.ObjectReference{Name: "cluster", Namespace: "cluster-ns"}},
			},
			seed:       []client.Object{crossCluster},
			wantErrs:   1,
			wantDetail: "cluster-ns/cluster",
		},
		{
			name: "same-namespace cluster with cacertSecret",
			obj: &weka.WekaClient{
				ObjectMeta: metav1.ObjectMeta{Namespace: "client-ns"},
				Spec:       weka.WekaClientSpec{TargetCluster: weka.ObjectReference{Name: "cluster", Namespace: "client-ns"}},
			},
			seed:     []client.Object{sameNsCluster},
			wantErrs: 0,
		},
		{
			name: "client sets its own cacertSecret",
			obj: &weka.WekaClient{
				ObjectMeta: metav1.ObjectMeta{Namespace: "client-ns"},
				Spec: weka.WekaClientSpec{
					TargetCluster: weka.ObjectReference{Name: "cluster", Namespace: "cluster-ns"},
					WekaHome:      &weka.WekahomeClientConfig{CacertSecret: "own-secret"},
				},
			},
			seed:     []client.Object{crossCluster},
			wantErrs: 0,
		},
		{
			name: "target cluster does not exist",
			obj: &weka.WekaClient{
				ObjectMeta: metav1.ObjectMeta{Namespace: "client-ns"},
				Spec:       weka.WekaClientSpec{TargetCluster: weka.ObjectReference{Name: "missing", Namespace: "cluster-ns"}},
			},
			seed:     nil,
			wantErrs: 0,
		},
		{
			name: "no targetCluster set",
			obj: &weka.WekaClient{
				ObjectMeta: metav1.ObjectMeta{Namespace: "client-ns"},
			},
			seed:     []client.Object{crossCluster},
			wantErrs: 0,
		},
		{
			name:     "wrong object type",
			obj:      &weka.WekaCluster{},
			seed:     nil,
			wantErrs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := cacertFakeClient(t, tt.seed...)
			errs := (clientWekahomeCacertNotInherited{}).Validate(context.Background(), c, tt.obj)
			if len(errs) != tt.wantErrs {
				t.Fatalf("errs = %v, want %d errors", errs, tt.wantErrs)
			}
			if tt.wantDetail != "" && !strings.Contains(errs[0].Detail, tt.wantDetail) {
				t.Fatalf("detail %q does not contain %q", errs[0].Detail, tt.wantDetail)
			}
		})
	}
}

func TestClientWekahomeCacertNotInherited_FieldPath(t *testing.T) {
	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "cluster-ns"},
		Spec:       weka.WekaClusterSpec{WekaHome: &weka.WekaHomeConfig{CacertSecret: "cluster-secret"}},
	}
	wc := &weka.WekaClient{
		ObjectMeta: metav1.ObjectMeta{Namespace: "client-ns"},
		Spec:       weka.WekaClientSpec{TargetCluster: weka.ObjectReference{Name: "cluster", Namespace: "cluster-ns"}},
	}

	errs := (&clientWekahomeCacertNotInherited{}).Validate(context.Background(), cacertFakeClient(t, cluster), wc)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %v", errs)
	}
	// The client sets no cacertSecret of its own here - that is the precondition for this warning -
	// so pointing at spec.wekaHome.cacertSecret would cite a field the user never wrote.
	if errs[0].Field != "spec.targetCluster" {
		t.Errorf("unexpected field path %q", errs[0].Field)
	}
}
