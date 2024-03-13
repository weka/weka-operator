package resources

import (
	"github.com/go-logr/zapr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	prettyconsole "github.com/thessem/zap-prettyconsole"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Container", func() {
	var c *wekav1alpha1.WekaContainer
	var factory *ContainerFactory

	BeforeEach(func() {
		c = &wekav1alpha1.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-container",
				Namespace: "default",
			},
			Spec: wekav1alpha1.ContainerSpec{
				Name:     "test-container",
				NodeName: "node1",

				Drives: []string{
					"/dev/nvme0n1",
					"/dev/nvme1n1",
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

		logger := zapr.NewLogger(prettyconsole.NewLogger(zap.DebugLevel))
		factory = NewContainerFactory(c, logger)
	})

	_ = Describe("NewDeployment", func() {
		var deployment *appsv1.Deployment

		BeforeEach(func() {
			var err error
			deployment, err = factory.NewDeployment()
			Expect(err).To(BeNil())
		})

		It("should create a new deployment", func() {
			Expect(deployment).NotTo(BeNil())
			Expect(deployment.Name).To(Equal("test-container"))
			Expect(deployment.Namespace).To(Equal("default"))
		})
	})
})
