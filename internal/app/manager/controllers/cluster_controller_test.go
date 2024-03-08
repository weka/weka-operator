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
		nodes := &v1.NodeList{}
		Eventually(func() error {
			err := k8sClient.List(TestCtx, nodes)
			return err
		}, time.Second*10, time.Millisecond*500).Should(BeNil())
		Expect(len(nodes.Items)).Should(Equal(1))

		actual := &wekav1alpha1.Cluster{}
		Eventually(func() bool {
			key := types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}
			err := k8sClient.Get(TestCtx, key, actual)
			return err == nil
		}, time.Second*10, time.Millisecond*500).Should(BeTrue())
		Expect(actual.Spec.SizeClass).Should(Equal("dev"))
		Expect(actual.Status.Nodes).Should(Equal([]string{"test-node"}))
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
	var iteration *iteration

	BeforeEach(func() {
		r := NewClusterReconciler(k8sManager)
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-cluster", Namespace: "default"}}
		iteration = newIteration(r, req)
	})

	AfterEach(func() {
	})

	Describe("refreshCluster", func() {
		BeforeEach(func() {
			iteration.refreshCluster(TestCtx)
		})
		It("should succeed", func() {
			Expect(iteration.refreshCluster(TestCtx)).Should(BeNil())
		})
		It("should set the cluster", func() {
			Expect(iteration.cluster.Name).Should(Equal("test-cluster"))
		})
	})

	Describe("createWekaNode", func() {
		var node *v1.Node
		var err error
		BeforeEach(func() {
			node = &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"weka.io/role": "backend",
					},
				},
			}
			iteration.cluster = &wekav1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							UID: "test-uid",
						},
					},
				},
			}
			err = iteration.createWekaNode(TestCtx, *node)
		})
		AfterEach(func() {
			k8sClient.Delete(TestCtx, node)
			Eventually(func() bool {
				dummy := &v1.Node{}
				err := k8sClient.Get(TestCtx, types.NamespacedName{Name: "test-node", Namespace: "default"}, dummy)
				return apierrors.IsNotFound(err)
			})
		})
		It("should succeed", func() {
			Expect(err).Should(BeNil())
		})
		It("should record the node name", func() {
			backends := iteration.backends
			Expect(len(backends.Items)).Should(Equal(1))
			Expect(backends.Items[0].Name).Should(Equal("test-node"))
		})
	})
})
