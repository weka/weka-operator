package controllers

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Weka Container Controller", func() {
	var ctx context.Context
	var subject *ContainerController
	BeforeEach(func() {
		Expect(k8sManager).NotTo(BeNil())
		ctx = context.Background()
		subject = NewContainerController(k8sManager)
	})

	Describe("NewContainerController", func() {
		It("should return a new ContainerController", func() {
			Expect(subject).NotTo(BeNil())
			Expect(subject.Client).NotTo(BeNil())
			Expect(subject.Scheme).NotTo(BeNil())
			Expect(subject.Logger).NotTo(BeNil())
		})
	})

	Describe("ContainerController", func() {
		Context("Without a container", func() {
			PIt("should do nothing", func() {
				result, err := subject.Reconcile(ctx, ctrl.Request{})
				Expect(err).To(BeNil())
				Expect(result).To(Equal(ctrl.Result{}))
			})
		})

		Context("when is a drive container", func() {
			var key client.ObjectKey
			var container *wekav1alpha1.WekaContainer
			BeforeEach(func() {
				key = client.ObjectKey{
					Namespace: "default",
					Name:      "test-container",
				}
				container = &wekav1alpha1.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: key.Namespace,
						Name:      key.Name,
					},
					Spec: wekav1alpha1.WekaContainerSpec{
						Mode:      "drive",
						CpuPolicy: wekav1alpha1.CpuPolicyDedicated,
					},
				}

				Expect(k8sClient.Create(ctx, container)).To(Succeed())
				Eventually(func() error {
					return k8sClient.Get(ctx, key, container)
				}).Should(Succeed())
			})

			AfterEach(func() {
				Expect(key.Namespace).NotTo(BeEmpty())
				container := &wekav1alpha1.WekaContainer{}
				if err := k8sClient.Get(ctx, key, container); err == nil {
					Expect(k8sClient.Delete(ctx, container)).To(Succeed())
				}
				Eventually(func() bool {
					err := k8sClient.Get(ctx, key, container)
					return apierrors.IsNotFound(err)
				}).Should(BeTrue())
			})

			Describe("Initial State", func() {
				It("should not set any conditions", func() {
					container := &wekav1alpha1.WekaContainer{}
					Expect(k8sClient.Get(ctx, key, container)).To(Succeed())
					Expect(container.Status.Conditions).To(HaveLen(0))
				})
			})
		})

		Context("when container has an invalid mode", func() {
			var key client.ObjectKey
			var container *wekav1alpha1.WekaContainer
			BeforeEach(func() {
				key = client.ObjectKey{
					Namespace: "default",
					Name:      "test-container",
				}
				container = &wekav1alpha1.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: key.Namespace,
						Name:      key.Name,
					},
					Spec: wekav1alpha1.WekaContainerSpec{
						Mode: "invalid",
					},
				}
			})

			AfterEach(func() {
				Expect(key.Namespace).NotTo(BeEmpty())
				container := &wekav1alpha1.WekaContainer{}
				if err := k8sClient.Get(ctx, key, container); err == nil {
					Expect(k8sClient.Delete(ctx, container)).To(Succeed())
				}
				Eventually(func() bool {
					err := k8sClient.Get(ctx, key, container)
					return apierrors.IsNotFound(err)
				}).Should(BeTrue())
			})

			Describe("Initial State", func() {
				It("should set an invalid condition", func() {
					Expect(k8sClient.Create(ctx, container)).NotTo(Succeed())
				})
			})
		})
	})
})
