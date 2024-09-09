package controllers

import (
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/testutil"
	"github.com/weka/weka-operator/pkg/util"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func testingManager() (testutil.Manager, error) {
	manager, err := testutil.TestingManager()
	if err != nil {
		return nil, err
	}
	manager.SetState(map[string]map[types.NamespacedName]client.Object{
		"*v1alpha1.WekaCluster": {
			types.NamespacedName{
				Name:      "test-cluster",
				Namespace: "test-namespace",
			}: &wekav1alpha1.WekaCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-namespace",
				},
				Spec: wekav1alpha1.WekaClusterSpec{
					Image:    "weka/weka:latest",
					Template: "small",
				},
			},
		},
		"*v1.ConfigMap": {
			types.NamespacedName{
				Name:      "weka-operator-allocmap",
				Namespace: util.DevModeNamespace,
			}: newAllocMap(),
		},
	})

	return manager, nil
}

func newAllocMap() *v1.ConfigMap {
	return &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "weka-operator-allocmap",
			Namespace: util.DevModeNamespace,
		},
		Data: map[string]string{
			"test": "test",
		},
	}
}
