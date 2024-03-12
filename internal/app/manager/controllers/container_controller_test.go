package controllers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("Integration Test", func() {
	var container *wekav1alpha1.WekaContainer

	BeforeEach(func() {
		container = newTestingContainer()
		Expect(k8sClient.Create(TestCtx, container)).Should(BeNil())
		Eventually(func() error {
			return k8sClient.Get(TestCtx, types.NamespacedName{Name: "test-container", Namespace: "default"}, container)
		}).Should(BeNil())
	})

	AfterEach(func() {
		k8sClient.Delete(TestCtx, container)
		Eventually(func() bool {
			dummy := &wekav1alpha1.WekaContainer{}
			err := k8sClient.Get(TestCtx, types.NamespacedName{Name: "test-container", Namespace: "default"}, dummy)
			return apierrors.IsNotFound(err)
		})
	})

	XIt("should create a deployment", func() {
		deployment := &appsv1.Deployment{}
		Eventually(func() error {
			return k8sClient.Get(TestCtx, types.NamespacedName{Name: "test-container", Namespace: "default"}, deployment)
		}).Should(BeNil())
		Expect(deployment.Name).Should(Equal("test-container"))
	})
})

var _ = XDescribe("ContainerController", func() {
	var subject *ContainerController
	BeforeEach(func() {
		subject = NewContainerController(k8sManager)
	})

	Describe("updateDeployment", func() {
		var deployment *appsv1.Deployment
		var err error
		BeforeEach(func() {
			deployment, err = resources.NewContainerFactory(newTestingContainer(), k8sManager.GetLogger()).NewDeployment()
			Expect(err).Should(BeNil())

			Expect(k8sClient.Create(TestCtx, deployment)).Should(BeNil())
			Eventually(func() error {
				key := types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace}
				return k8sClient.Get(TestCtx, key, deployment)
			}).Should(BeNil())
		})

		It("should update a deployment", func() {
			newDeployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "default",
					Labels: map[string]string{
						"deployment": "new-deployment",
					},
				},
			}
			Expect(subject.updateDeployment(TestCtx, newDeployment)).Should(BeNil())
			updated := &appsv1.Deployment{}
			Eventually(func() string {
				err := k8sClient.Get(TestCtx, types.NamespacedName{Name: "test-deployment", Namespace: "default"}, updated)
				if err != nil {
					return ""
				}
				return updated.Labels["deployment"]
			}).Should(Equal("new-deployment"))
		})
	})
})

func newTestingContainer() *wekav1alpha1.WekaContainer {
	return &wekav1alpha1.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-container",
			Namespace: "default",
		},
		Spec: wekav1alpha1.ContainerSpec{
			Name:     "test-container",
			NodeName: "node1",
			Drives: []string{
				"/dev/nvme0n1",
			},
			Image:               "wekaco/weka:latest",
			ImagePullSecretName: "weka-registry",
			WekaVersion:         "4.2.7.64-k8so-beta.10",
			BackendIP:           "10.1.2.3",
			//WekaUsername: v1.EnvVarSource{
			//SecretKeyRef: &v1.SecretKeySelector{
			//Key: "username",
			//LocalObjectReference: v1.LocalObjectReference{
			//Name: "weka-credentials",
			//},
			//},
			//},
			//WekaPassword: v1.EnvVarSource{
			//SecretKeyRef: &v1.SecretKeySelector{
			//Key: "password",
			//LocalObjectReference: v1.LocalObjectReference{
			//Name: "weka-credentials",
			//},
			//},
			//},
		},
	}
}
