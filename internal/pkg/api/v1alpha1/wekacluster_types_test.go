package v1alpha1

import (
	"testing"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGetOperatorSecretName(t *testing.T) {
	cluster := WekaCluster{
		ObjectMeta: v1.ObjectMeta{
			UID: "1234-5678",
		},
	}

	expected := "weka-operator-1234-5678"
	actual := cluster.GetOperatorSecretName()

	if actual != expected {
		t.Errorf("Expected %s, got %s", expected, actual)
	}
}

func TestGetLastGuidPart(t *testing.T) {
	cluster := WekaCluster{
		ObjectMeta: v1.ObjectMeta{
			UID: "1234-5678",
		},
	}

	expected := "5678"
	actual := cluster.GetLastGuidPart()

	if actual != expected {
		t.Errorf("Expected %s, got %s", expected, actual)
	}
}

func TestGetUserClusterUsername(t *testing.T) {
	cluster := WekaCluster{
		ObjectMeta: v1.ObjectMeta{
			UID: "1234-5678",
		},
	}

	expected := "weka5678"
	actual := cluster.GetUserClusterUsername()

	if actual != expected {
		t.Errorf("Expected %s, got %s", expected, actual)
	}
}

func TestGetOperatorClusterUsername(t *testing.T) {
	cluster := WekaCluster{
		ObjectMeta: v1.ObjectMeta{
			UID: "1234-5678",
		},
	}

	expected := "weka-operator-5678"
	actual := cluster.GetOperatorClusterUsername()

	if actual != expected {
		t.Errorf("Expected %s, got %s", expected, actual)
	}
}

func TestGetUserSecretName(t *testing.T) {
	cluster := WekaCluster{
		ObjectMeta: v1.ObjectMeta{
			Name: "test-cluster",
		},
	}

	expected := "weka-cluster-test-cluster"
	actual := cluster.GetUserSecretName()

	if actual != expected {
		t.Errorf("Expected %s, got %s", expected, actual)
	}
}
