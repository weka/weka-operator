package controllers

import (
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/weka/weka-operator/internal/app/manager/domain"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

var _ = Describe("Cluster Controller", func() {
	var logger logr.Logger
	BeforeEach(func() {
		logger = k8sManager.GetLogger().WithName("Cluster Controller")
	})

	_ = Describe("Reconcile", func() {
		var cluster *wekav1alpha1.Cluster
		var node *v1.Node
		BeforeEach(func() {
			node = &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-node",
					Namespace: "default",
					Labels: map[string]string{
						"weka.io/role": "backend",
					},
				},
			}
			Expect(k8sClient.Create(TestCtx, node)).Should(BeNil())
			Eventually(func() error {
				return k8sClient.Get(TestCtx, types.NamespacedName{Name: "test-node", Namespace: "default"}, node)
			}).Should(BeNil())
			Expect(node.Name).Should(Equal("test-node"))
			Expect(node.Labels["weka.io/role"]).Should(Equal("backend"))

			cluster = &wekav1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: wekav1alpha1.ClusterSpec{
					SizeClass: "dev",
				},
			}
			Expect(k8sClient.Create(TestCtx, cluster)).Should(BeNil())
		})

		AfterEach(func() {
			backends := &wekav1alpha1.BackendList{}
			Expect(k8sClient.List(TestCtx, backends)).Should(BeNil())
			for _, backend := range backends.Items {
				k8sClient.Delete(TestCtx, &backend)
				Eventually(func() bool {
					dummy := &wekav1alpha1.Backend{}
					err := k8sClient.Get(TestCtx, types.NamespacedName{Name: backend.Name, Namespace: backend.Namespace}, dummy)
					return apierrors.IsNotFound(err)
				})
			}

			k8sClient.Delete(TestCtx, cluster)
			Eventually(func() bool {
				dummy := &wekav1alpha1.Cluster{}
				err := k8sClient.Get(TestCtx, types.NamespacedName{Name: "test-cluster", Namespace: "default"}, dummy)
				return apierrors.IsNotFound(err)
			})

			k8sClient.Delete(TestCtx, node)
			Eventually(func() bool {
				dummy := &v1.Node{}
				err := k8sClient.Get(TestCtx, types.NamespacedName{Name: "test-node", Namespace: "default"}, dummy)
				return apierrors.IsNotFound(err)
			})
		})

		It("should create a cluster", func() {
			Expect(cluster.Spec.SizeClass).Should(Equal("dev"))
		})

		PIt("should create one backend", func() {
			allBackends := &wekav1alpha1.BackendList{}

			Eventually(func() error {
				err := k8sClient.List(TestCtx, allBackends)
				return err
			}, time.Second*10, time.Millisecond*500).Should(BeNil())
			Expect(len(allBackends.Items)).Should(Equal(1))

			actual := &wekav1alpha1.Backend{}
			deployKey := types.NamespacedName{Name: "test-node", Namespace: "default"}
			Eventually(func() error {
				err := k8sClient.Get(TestCtx, deployKey, actual)
				return err
			}, time.Second*10, time.Millisecond*500).Should(BeNil())

			Expect(actual.Name).Should(Equal("test-node"))
		})
	})

	_ = Describe("iteration", func() {
		var cluster *wekav1alpha1.Cluster
		var iteration *iteration

		BeforeEach(func() {
			logger = logger.WithName("iteration")
			cluster = &wekav1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: wekav1alpha1.ClusterSpec{
					SizeClass: "dev",
				},
			}
			Expect(k8sClient.Create(TestCtx, cluster)).Should(BeNil())
			Eventually(func() error {
				return k8sClient.Get(TestCtx, types.NamespacedName{Name: "test-cluster", Namespace: "default"}, cluster)
			}).Should(BeNil())

			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-cluster", Namespace: "default"}}
			iteration = newIteration(req, logger)
			iteration.cluster = cluster
		})

		AfterEach(func() {
			k8sClient.Delete(TestCtx, cluster)
			Eventually(func() bool {
				dummy := &wekav1alpha1.Cluster{}
				err := k8sClient.Get(TestCtx, types.NamespacedName{Name: "test-cluster", Namespace: "default"}, dummy)
				return apierrors.IsNotFound(err)
			})
		})

		PDescribe("refreshNodes", func() {
			nodes := &v1.NodeList{}

			var scheduler *domain.Scheduling
			BeforeEach(func() {
				node := &v1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
						Labels: map[string]string{
							"weka.io/role": "backend",
						},
					},
				}
				Expect(k8sClient.Create(TestCtx, node)).Should(BeNil())
				Eventually(func() error {
					return k8sClient.Get(TestCtx, types.NamespacedName{Name: "test-node", Namespace: "default"}, node)
				}).Should(BeNil())
				nodes.Items = append(nodes.Items, *node)

				var err error
				logger = logger.WithName("refreshNodes")
				scheduler, err = iteration.schedulerForNodes(logger)
				Expect(err).Should(BeNil())
			})

			AfterEach(func() {
				for _, node := range nodes.Items {
					k8sClient.Delete(TestCtx, &node)
					Eventually(func() bool {
						dummy := &v1.Node{}
						err := k8sClient.Get(TestCtx, types.NamespacedName{Name: "test-node", Namespace: "default"}, dummy)
						return apierrors.IsNotFound(err)
					})
				}
			})

			It("populate backends", func() {
				Expect(len(scheduler.Backends())).Should(Equal(1))
				Expect(scheduler.Backends()[0].Name).Should(Equal("test-node"))
			})
		})

		Describe("newBackendForNode", func() {
			node := &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"weka.io/role": "backend",
					},
				},
			}
			var backend *wekav1alpha1.Backend
			BeforeEach(func() {
				var err error
				backend, err = iteration.newBackendForNode(node)
				Expect(err).Should(BeNil())
			})

			It("should create a backend", func() {
				Expect(backend.Name).Should(Equal("test-node"))
				Expect(backend.Namespace).Should(Equal("default"))
				Expect(backend.Labels["weka.io/cluster"]).Should(Equal("test-cluster"))
				Expect(backend.Labels["weka.io/node"]).Should(Equal("test-node"))
				Expect(backend.OwnerReferences[0].Name).Should(Equal("test-cluster"))
			})
			PIt("should have a drive assignment", func() {
				Expect(len(backend.Status.DriveAssignments)).Should(Equal(1))
			})
		})

		Describe("reconcileContainers", func() {
			BeforeEach(func() {
			})
		})
	})
})
