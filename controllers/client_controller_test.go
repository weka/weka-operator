package controllers

import (
	"time"

	"github.com/kubernetes-sigs/kernel-module-management/api/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	wekav1alpha1 "github.com/weka/weka-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("Client controller", func() {
	var client *wekav1alpha1.Client
	testNamespace := "default"

	BeforeEach(func() {
		client = &wekav1alpha1.Client{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-client",
				Namespace: testNamespace,
			},
			Spec: wekav1alpha1.ClientSpec{
				Version:             "1.0.0",
				Image:               "wekadev/weka-operator",
				ImagePullSecretName: "test-secret",
				BackendIP:           "1.2.3.4",
			},
		}
	})

	Context("When creating a client", func() {
		var clientCreated error
		timeout := time.Second * 10
		interval := time.Millisecond * 250

		BeforeEach(func() {
			// Clean up existing clients in case of failed runs
			existingClients := &wekav1alpha1.ClientList{}
			k8sClient.List(testCtx, existingClients)
			for _, existingClient := range existingClients.Items {
				k8sClient.Delete(testCtx, &existingClient)
			}

			clientCreated = k8sClient.Create(testCtx, client)
		})
		AfterEach(func() {
			k8sClient.Delete(testCtx, client)
		})
		It("should create a client", func() {
			Expect(clientCreated).To(Succeed())
		})
		It("should create a wekafsio module", func() {
			createdModule := &v1beta1.Module{}
			deployKey := types.NamespacedName{
				Name:      "wekafsio",
				Namespace: testNamespace,
			}
			Eventually(func() bool {
				err := k8sClient.Get(testCtx, deployKey, createdModule)
				return err == nil
			}, timeout, interval).Should(BeTrue())
			Expect(createdModule.Spec.ModuleLoader.Container.Modprobe.ModuleName).To(Equal("wekafsio"))
		})
	})
})
