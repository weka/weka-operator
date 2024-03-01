package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

var _ = Describe("Reconcile", func() {
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
		Eventually(func() error {
			return k8sClient.Get(TestCtx, types.NamespacedName{Name: "test-cluster", Namespace: "default"}, cluster)
		}).Should(BeNil())
	})

	AfterEach(func() {
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

	XIt("should create a cluster", func() {
		Expect(cluster.Spec.SizeClass).Should(Equal("dev"))
		Expect(len(cluster.Status.Nodes)).Should(Equal(1))
		Expect(cluster.Status.Nodes).Should(Equal([]string{"test-node"}))
	})

	XIt("should create one backend", func() {
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

var _ = Describe("iteration", func() {
	var cluster *wekav1alpha1.Cluster
	var iteration *iteration

	BeforeEach(func() {
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
		iteration = newIteration(req)
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

	Describe("refreshNodes", func() {
		nodes := &v1.NodeList{}

		listNodes := func(list *v1.NodeList) error {
			list.Items = make([]v1.Node, 1)
			list.Items[0] = v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"weka.io/role": "backend",
					},
				},
			}
			return nil
		}

		var backendNodes []v1.Node
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
			backendNodes, err = iteration.refreshNodes(listNodes)
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
			Expect(len(backendNodes)).Should(Equal(1))
			Expect(backendNodes[0].Name).Should(Equal("test-node"))
		})
	})
})
