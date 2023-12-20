package controllers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	wekav1alpha1 "github.com/weka/weka-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
				IONodeCount:         1,
				ManagementIPs:       "5.6.7.8",
				ImagePullSecretName: "test-secret",

				Backend: wekav1alpha1.BackendSpec{
					IP: "1.2.3.4",
				},
			},
		}
	})

	It("should create a client", func() {
		Expect(k8sClient.Create(testCtx, client)).Should(Succeed())
	})
})
