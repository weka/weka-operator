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
)

var _ = Describe("Reconcile", func() {
	var cluster *wekav1alpha1.Cluster
	var node *v1.Node
	BeforeEach(func() {
		node = &v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-node",
				Namespace: "default",
			},
		}
		Expect(k8sClient.Create(TestCtx, node)).Should(BeNil())
		cluster = &wekav1alpha1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "default",
			},
			Spec: wekav1alpha1.ClusterSpec{
				SizeClass: "dev",
			},
		}
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

	It("should have one node", func() {
		actual := &v1.Node{}
		Eventually(func() bool {
			err := k8sClient.Get(TestCtx, types.NamespacedName{Name: "test-node", Namespace: "default"}, actual)
			return err == nil
		}, time.Second*10, time.Millisecond*500).Should(BeTrue())
		Expect(actual.Name).Should(Equal("test-node"))
	})

	_ = Describe("SizeClasses", func() {
		It("should have 4 size classes", func() {
			Expect(len(SizeClasses)).Should(Equal(4))
		})
		It("should lookup by name", func() {
			Expect(SizeClasses[cluster.Spec.SizeClass].ContainerCount).Should(Equal(1))
		})
	})

	It("should create a cluster", func() {
		Expect(k8sClient.Create(TestCtx, cluster)).Should(BeNil())

		actual := &wekav1alpha1.Cluster{}
		Eventually(func() bool {
			err := k8sClient.Get(TestCtx, types.NamespacedName{Name: "test-cluster", Namespace: "default"}, actual)
			return err == nil
		}, time.Second*10, time.Millisecond*500).Should(BeTrue())
		Expect(actual.Spec.SizeClass).Should(Equal("dev"))
	})

	XIt("should create one backend", func() {
		Expect(k8sClient.Create(TestCtx, cluster)).Should(BeNil())

		actual := &wekav1alpha1.Backend{}
		deployKey := types.NamespacedName{Name: "test-backend", Namespace: "default"}

		Eventually(func() error {
			err := k8sClient.Get(TestCtx, deployKey, actual)
			return err
		}, time.Second*10, time.Millisecond*500).Should(BeNil())

		Expect(actual.Name).Should(Equal("test-backend"))
	})
})
