package controllers

import (
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/testutil"
	"github.com/weka/weka-operator/pkg/util"
	"gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var testingPodNamespace string = "weka-operator-system"

func testingManager() (testutil.Manager, error) {
	testingCluster := &wekav1alpha1.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-namespace",
		},
		Spec: wekav1alpha1.WekaClusterSpec{
			Image:    "weka/weka:latest",
			Template: "small",
		},
	}
	manager, err := testutil.TestingManager()
	if err != nil {
		return nil, err
	}
	state := map[string]map[types.NamespacedName]client.Object{
		"*v1alpha1.WekaCluster": {
			types.NamespacedName{
				Name:      "test-cluster",
				Namespace: "test-namespace",
			}: testingCluster,
		},
		"*v1.ConfigMap": {
			types.NamespacedName{
				Name:      "weka-operator-allocmap",
				Namespace: util.DevModeNamespace,
			}: newAllocMap(),
			types.NamespacedName{
				Namespace: testingPodNamespace,
				Name:      bootScriptConfigName,
			}: bootScriptConfigMap(),
		},
		"*v1.Secret": {
			types.NamespacedName{
				Name:      testingCluster.GetOperatorSecretName(),
				Namespace: "test-namespace",
			}: &v1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testingCluster.GetOperatorSecretName(),
					Namespace: "test-namespace",
				},
				Data: map[string][]byte{},
			},
		},
		"*v1.Node": {},
	}

	for _, nodeName := range []string{"node-1", "node-2", "node-3"} {
		state["*v1.Node"][types.NamespacedName{Name: nodeName}] = newWorkerNode(nodeName)
	}

	manager.SetState(state)
	return manager, nil
}

func newAllocMap() *v1.ConfigMap {
	allocations := &allocator.Allocations{}
	yamlData, err := yaml.Marshal(allocations)
	if err != nil {
		return nil
	}
	compressedYamlData, err := util.CompressBytes(yamlData)
	if err != nil {
		return nil
	}
	return &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "weka-operator-allocmap",
			Namespace: util.DevModeNamespace,
		},
		BinaryData: map[string][]byte{
			"allocmap.yaml": compressedYamlData,
		},
	}
}

// bootScriptConfigMap returns a config map with the boot scripts
func bootScriptConfigMap() *v1.ConfigMap {
	return &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      bootScriptConfigName,
			Namespace: testingPodNamespace,
		},
	}
}

func newWorkerNode(nodeName string) *v1.Node {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Labels: map[string]string{
				"node-type": "backend",
			},
		},
		Status: v1.NodeStatus{
			NodeInfo: v1.NodeSystemInfo{
				BootID: "boot-id",
			},
		},
	}
}
