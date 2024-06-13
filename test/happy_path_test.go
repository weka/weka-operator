package test

import "testing"

func TestHappyPath(t *testing.T) {
	for _, cluster := range e2eTest.Clusters {
		test := (&Cluster{ClusterTest: *cluster}).ValidateStartupCompleted(pkgCtx)
		t.Run(cluster.Cluster.Name, test)
	}
}
