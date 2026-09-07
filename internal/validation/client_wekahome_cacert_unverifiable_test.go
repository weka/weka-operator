package validation

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestClientWekahomeCacertUnverifiable(t *testing.T) {
	cases := []struct {
		name          string
		wekaHome      *weka.WekahomeClientConfig
		targetCluster weka.ObjectReference
		wantErrs      int
	}{
		{name: "wekaHome unset", wekaHome: nil, wantErrs: 0},
		{name: "wekaHome set, cacertSecret empty", wekaHome: &weka.WekahomeClientConfig{}, wantErrs: 0},
		{
			name:     "cacertSecret set, no targetCluster",
			wekaHome: &weka.WekahomeClientConfig{CacertSecret: "my-ca"},
			wantErrs: 1,
		},
		{
			name:          "cacertSecret set, targetCluster set",
			wekaHome:      &weka.WekahomeClientConfig{CacertSecret: "my-ca"},
			targetCluster: weka.ObjectReference{Name: "cluster", Namespace: "ns"},
			wantErrs:      0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wc := &weka.WekaClient{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}}
			wc.Spec.WekaHome = tc.wekaHome
			wc.Spec.TargetCluster = tc.targetCluster

			v := clientWekahomeCacertUnverifiable{}
			errs := v.Validate(context.Background(), nil, wc)
			if len(errs) != tc.wantErrs {
				t.Fatalf("expected %d violations, got %d: %v", tc.wantErrs, len(errs), errs)
			}
			for _, e := range errs {
				if e.Field != "spec.wekaHome.cacertSecret" {
					t.Errorf("unexpected field path %q", e.Field)
				}
			}
		})
	}
}

func TestClientWekahomeCacertUnverifiable_WrongType(t *testing.T) {
	v := clientWekahomeCacertUnverifiable{}
	if errs := v.Validate(context.Background(), nil, &weka.WekaCluster{}); errs != nil {
		t.Fatalf("expected nil for non-WekaClient object, got %v", errs)
	}
}
