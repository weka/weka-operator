package domain_test

import (
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/zap"

	"github.com/weka/weka-operator/internal/app/manager/domain"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TODO: Update scheduling to account for cores
var _ = Describe("Scheduling", func() {
	var scheduling *domain.Scheduling
	var logger logr.Logger
	container := &v1.LocalObjectReference{Name: "test-container"}

	BeforeEach(func() {
		zapLog, err := zap.NewDevelopment()
		Expect(err).ShouldNot(HaveOccurred())

		logger = zapr.NewLogger(zapLog).WithName("Scheduling")
	})

	Context("when no nodes in use", func() {
		var nodePool []*wekav1alpha1.Backend
		var cluster *wekav1alpha1.Cluster
		var err error
		BeforeEach(func() {
			nodePool = []*wekav1alpha1.Backend{
				unusedBackend(),
			}
			cluster = devCluster()
			logger = logger.WithName("no-nodes-in-use")
			scheduling, err = domain.ForCluster(cluster, nodePool, logger)
			Expect(err).ShouldNot(HaveOccurred())
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
				Expect(cluster.Status.Backends).Should(HaveLen(0))
				scheduling.AssignBackends(container)
			})

			It("should assign backends for nodes", func() {
				Expect(cluster.Status.Backends).Should(HaveLen(1))
			})
		})

		Describe("HasFreeDrives", func() {
			BeforeEach(func() {
				Expect(scheduling).ShouldNot(BeNil())
			})

			It("should return true for a node with free drives", func() {
				Expect(scheduling.HasFreeDrives(nodePool[0])).Should(BeTrue())
			})
		})

		Describe("AssignToDrive", func() {
			BeforeEach(func() {
				Expect(scheduling).ShouldNot(BeNil())
			})

			It("should assign a container to a drive", func() {
				backend := nodePool[0]
				container := &v1.LocalObjectReference{Name: "test-container"}
				Expect(scheduling.AssignToDrive(backend, container)).Should(BeNil())
			})
		})
	})

	Context("when nodes are in use", func() {
		var nodePool []*wekav1alpha1.Backend
		var cluster *wekav1alpha1.Cluster
		var err error
		BeforeEach(func() {
			nodePool = []*wekav1alpha1.Backend{
				allocatedBackend(),
				unusedBackend(),
			}
			cluster = devCluster()
			logger = logger.WithName("nodes-in-use")
			scheduling, err = domain.ForCluster(cluster, nodePool, logger)
			Expect(err).ShouldNot(HaveOccurred())
		})

		Describe("AssignBackends", func() {
			BeforeEach(func() {
				Expect(scheduling).ShouldNot(BeNil())
				Expect(cluster.Status.Backends).Should(HaveLen(0))
				scheduling.AssignBackends(container)
			})

			It("should assign backends for nodes", func() {
				Expect(cluster.Status.Backends).Should(HaveLen(1))
			})
		})

		Describe("HasFreeDrives", func() {
			BeforeEach(func() {
				Expect(scheduling).ShouldNot(BeNil())
			})

			It("should return true for a node with free drives", func() {
				Expect(scheduling.HasFreeDrives(nodePool[0])).Should(BeFalse())
				Expect(scheduling.HasFreeDrives(nodePool[1])).Should(BeTrue())
			})
		})

		Describe("AssignToDrive", func() {
			BeforeEach(func() {
				Expect(scheduling).ShouldNot(BeNil())
			})

			It("should assign a container to a drive", func() {
				container := &v1.LocalObjectReference{Name: "test-container"}
				Expect(scheduling.AssignToDrive(nodePool[0], container)).Should(MatchError(&domain.InsufficientDrivesError{}))
				Expect(scheduling.AssignToDrive(nodePool[1], container)).Should(BeNil())
			})
		})
	})

	Context("when too few nodes", func() {
		var nodePool []*wekav1alpha1.Backend
		var cluster *wekav1alpha1.Cluster
		Context("becuase node pool too small", func() {
			BeforeEach(func() {
				nodePool = []*wekav1alpha1.Backend{}
				cluster = devCluster()
				logger = logger.WithName("too-few-nodes")

				var err error
				scheduling, err = domain.ForCluster(cluster, nodePool, logger)
				Expect(err).ShouldNot(HaveOccurred())
			})

			Describe("AssignBackends", func() {
				BeforeEach(func() {
					Expect(scheduling).ShouldNot(BeNil())
					Expect(scheduling.Backends()).Should(HaveLen(0))
					Expect(scheduling.ContainerCount()).Should(Equal(1))
					Expect(cluster.Status.Backends).Should(HaveLen(0))
					Expect(scheduling.AssignBackends(container)).Should(
						MatchError(
							&domain.InsufficientNodesError{
								Wanted: 1,
								Found:  0,
							}))
				})
				It("should not assign any backends", func() {
					Expect(cluster.Status.Backends).Should(HaveLen(0))
				})
			})
		})

		Context("because too many nodes allocated", func() {
			BeforeEach(func() {
				nodePool = []*wekav1alpha1.Backend{
					unusedBackend(),
					unusedBackend(),
					unusedBackend(),
					unusedBackend(),
					unusedBackend(),
					unusedBackend(),
					allocatedBackend(),
				}
				cluster = largeCluster()
				logger = logger.WithName("too-many-allocated")
				var err error
				scheduling, err = domain.ForCluster(cluster, nodePool, logger)
				Expect(err).ShouldNot(HaveOccurred())
			})

			Describe("AssignBackends", func() {
				BeforeEach(func() {
					Expect(scheduling).ShouldNot(BeNil())
					Expect(cluster.Status.Backends).Should(HaveLen(0))
					Expect(scheduling.AssignBackends(container)).Should(
						MatchError(
							&domain.InsufficientNodesError{
								Wanted: 7,
								Found:  6,
							}))
				})
				It("should not assign any backends", func() {
					Expect(cluster.Status.Backends).Should(HaveLen(0))
				})
			})
		})
	})

	Context("when too many nodes", func() {
		var nodePool []*wekav1alpha1.Backend
		var cluster *wekav1alpha1.Cluster
		BeforeEach(func() {
			nodePool = []*wekav1alpha1.Backend{
				unusedBackend(),
				unusedBackend(),
			}
			cluster = devCluster()
			logger = logger.WithName("too-many-nodes")
			var err error
			scheduling, err = domain.ForCluster(cluster, nodePool, logger)
			Expect(err).ShouldNot(HaveOccurred())
		})
		Describe("AssignBackends", func() {
			BeforeEach(func() {
				Expect(scheduling).ShouldNot(BeNil())
				Expect(cluster.Status.Backends).Should(HaveLen(0))
				scheduling.AssignBackends(container)
			})
			It("should only assign needed backends", func() {
				Expect(cluster.Status.Backends).Should(HaveLen(1))
			})
		})
	})

	Context("when nodes are shared", func() {
		var nodePool []*wekav1alpha1.Backend
		var thisCluster *wekav1alpha1.Cluster
		var otherCluster *wekav1alpha1.Cluster
		var schedulingThisCluster *domain.Scheduling
		var schedulingOtherCluster *domain.Scheduling
		BeforeEach(func() {
			nodePool = []*wekav1alpha1.Backend{
				unusedBackend(),
			}
			thisCluster = devCluster()
			otherCluster = devCluster()
			logger = logger.WithName("shared-nodes")
			var err error
			schedulingThisCluster, err = domain.ForCluster(thisCluster, nodePool, logger)
			Expect(err).ShouldNot(HaveOccurred())

			schedulingOtherCluster, err = domain.ForCluster(otherCluster, nodePool, logger)
			Expect(err).ShouldNot(HaveOccurred())
		})
		Describe("AssignBackends", func() {
			BeforeEach(func() {
				Expect(otherCluster.Status.Backends).Should(HaveLen(0))
				schedulingOtherCluster.AssignBackends(container)

				Expect(thisCluster.Status.Backends).Should(HaveLen(0))
				schedulingThisCluster.AssignBackends(container)
			})
			It("should only assign needed backends", func() {
				Expect(thisCluster.Status.Backends).Should(HaveLen(0))
				Expect(otherCluster.Status.Backends).Should(HaveLen(1))
			})
		})
	})

	Context("when invalid size class", func() {
		var nodePool []*wekav1alpha1.Backend
		var cluster *wekav1alpha1.Cluster
		BeforeEach(func() {
			nodePool = []*wekav1alpha1.Backend{
				unusedBackend(),
			}
			cluster = &wekav1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: wekav1alpha1.ClusterSpec{
					SizeClass: "invalid",
				},
			}
			var err error
			scheduling, err = domain.ForCluster(cluster, nodePool, logger.WithName("invalid-size-class"))
			Expect(err).ShouldNot(HaveOccurred())
		})
		Describe("AssignBackends", func() {
			BeforeEach(func() {
				Expect(scheduling).ShouldNot(BeNil())
				Expect(cluster.Status.Backends).Should(HaveLen(0))
				Expect(scheduling.AssignBackends(container)).Should(MatchError(&domain.InvalildSizeClassError{}))
			})
			It("should not assign any backends", func() {
				Expect(cluster.Status.Backends).Should(HaveLen(0))
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

func unusedBackend() *wekav1alpha1.Backend {
	return &wekav1alpha1.Backend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-node-1",
			Namespace: "default",
		},
		Spec: wekav1alpha1.BackendSpec{
			NodeName: "test-node-1",
		},
		Status: wekav1alpha1.BackendStatus{
			DriveAssignments: map[wekav1alpha1.DriveName]*v1.LocalObjectReference{
				"/dev/nvme0n1": {},
			},
			CoreAssignments: map[wekav1alpha1.CoreId]*v1.LocalObjectReference{
				"0": {},
			},
		},
	}
}

func allocatedBackend() *wekav1alpha1.Backend {
	return &wekav1alpha1.Backend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-node-2",
			Namespace: "default",
		},
		Spec: wekav1alpha1.BackendSpec{
			NodeName: "test-node-2",
		},
		Status: wekav1alpha1.BackendStatus{
			DriveAssignments: map[wekav1alpha1.DriveName]*v1.LocalObjectReference{
				"/dev/nvme0n1": {
					Name: "other-cluster-container-0",
				},
			},
			CoreAssignments: map[wekav1alpha1.CoreId]*v1.LocalObjectReference{
				"0": {
					Name: "other-cluster-container-0",
				},
			},
		},
	}
}
