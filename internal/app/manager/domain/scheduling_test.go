package domain_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/weka/weka-operator/internal/app/manager/domain"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TODO: Update scheduling to account for drives and cores
var _ = Describe("Scheduling", func() {
	var scheduling *domain.Scheduling

	Context("when no nodes in use", func() {
		var nodePool []v1.Node
		var cluster *wekav1alpha1.Cluster
		BeforeEach(func() {
			nodePool = []v1.Node{
				unusedBackend(),
			}
			cluster = devCluster()
			scheduling = domain.ForCluster(cluster, nodePool)
		})

		Describe("ForCluster", func() {
			It("should return a new instance of Scheduling", func() {
				Expect(scheduling).ShouldNot(BeNil())
			})
			It("should have the correct cluster", func() {
				Expect(scheduling).ShouldNot(BeNil())
				Expect(scheduling.Cluster()).Should(Equal(cluster))
			})
		})

		Describe("AssignBackends", func() {
			BeforeEach(func() {
				Expect(scheduling).ShouldNot(BeNil())
				Expect(cluster.Status.Nodes).Should(HaveLen(0))
				scheduling.AssignBackends()
			})

			It("should assign backends for nodes", func() {
				Expect(cluster.Status.Nodes).Should(HaveLen(1))
			})
		})
	})

	Context("when nodes are in use", func() {
		var nodePool []v1.Node
		var cluster *wekav1alpha1.Cluster
		BeforeEach(func() {
			nodePool = []v1.Node{
				unusedBackend(),
				allocatedBackend(),
			}
			cluster = devCluster()
			scheduling = domain.ForCluster(cluster, nodePool)
		})

		Describe("AssignBackends", func() {
			BeforeEach(func() {
				Expect(scheduling).ShouldNot(BeNil())
				Expect(cluster.Status.Nodes).Should(HaveLen(0))
				scheduling.AssignBackends()
			})

			It("should assign backends for nodes", func() {
				Expect(cluster.Status.Nodes).Should(HaveLen(1))
			})
		})
	})

	Context("when node pool has non-backend nodes", func() {
		var nodePool []v1.Node
		var cluster *wekav1alpha1.Cluster
		BeforeEach(func() {
			nodePool = []v1.Node{
				unusedBackend(),
				nonBackend(),
			}
			cluster = devCluster()
			scheduling = domain.ForCluster(cluster, nodePool)
		})

		Describe("AssignBackends", func() {
			BeforeEach(func() {
				Expect(scheduling).ShouldNot(BeNil())
				Expect(cluster.Status.Nodes).Should(HaveLen(0))
				scheduling.AssignBackends()
			})

			It("should assign backends for nodes", func() {
				Expect(cluster.Status.Nodes).Should(HaveLen(1))
			})
		})
	})

	Context("when too few nodes", func() {
		var nodePool []v1.Node
		var cluster *wekav1alpha1.Cluster
		BeforeEach(func() {
			nodePool = []v1.Node{
				unusedBackend(),
				unusedBackend(),
				unusedBackend(),
				unusedBackend(),
				unusedBackend(),
				unusedBackend(),
				allocatedBackend(),
			}
			cluster = largeCluster()
			scheduling = domain.ForCluster(cluster, nodePool)
		})

		Describe("AssignBackends", func() {
			BeforeEach(func() {
				Expect(scheduling).ShouldNot(BeNil())
				Expect(cluster.Status.Nodes).Should(HaveLen(0))
				Expect(scheduling.AssignBackends()).Should(MatchError(&domain.InsufficientNodesError{}))
			})
			It("should not assign any backends", func() {
				Expect(cluster.Status.Nodes).Should(HaveLen(0))
			})
		})
	})

	Context("when too many nodes", func() {
		var nodePool []v1.Node
		var cluster *wekav1alpha1.Cluster
		BeforeEach(func() {
			nodePool = []v1.Node{
				unusedBackend(),
				unusedBackend(),
			}
			cluster = devCluster()
			scheduling = domain.ForCluster(cluster, nodePool)
		})
		Describe("AssignBackends", func() {
			BeforeEach(func() {
				Expect(scheduling).ShouldNot(BeNil())
				Expect(cluster.Status.Nodes).Should(HaveLen(0))
				scheduling.AssignBackends()
			})
			It("should only assign needed backends", func() {
				Expect(cluster.Status.Nodes).Should(HaveLen(1))
			})
		})
	})
})

func devCluster() *wekav1alpha1.Cluster {
	return &wekav1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: wekav1alpha1.ClusterSpec{
			SizeClass: "dev",
		},
	}
}

func largeCluster() *wekav1alpha1.Cluster {
	return &wekav1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: wekav1alpha1.ClusterSpec{
			SizeClass: "large",
		},
	}
}

func unusedBackend() v1.Node {
	return v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-node-1",
			Namespace: "default",
			Labels: map[string]string{
				"weka.io/role": "backend",
			},
		},
	}
}

func allocatedBackend() v1.Node {
	return v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-node-2",
			Namespace: "default",
			Labels: map[string]string{
				"weka.io/role":    "backend",
				"weka.io/cluster": "other-cluster",
			},
		},
	}
}

func nonBackend() v1.Node {
	return v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-node-3",
			Namespace: "default",
			Labels: map[string]string{
				"weka.io/role": "unspecified",
			},
		},
	}
}
