package wekacontainer

import (
	"context"
	"encoding/json"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/weka/go-weka-observability/instrumentation"
	obslogger "github.com/weka/go-weka-observability/logger"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/controllers/resources"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/services/discovery"
)

var otelShutdown func(context.Context) error

func TestEnsurePodDriversBuilder(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "EnsurePod DriversBuilder Suite")
}

var _ = BeforeSuite(func() {
	ctx := context.Background()
	logger := obslogger.CreateLogger(obslogger.WithConsoleSink(), obslogger.WithDebugLevel())

	var err error
	otelShutdown, err = instrumentation.SetupOTelSDKWithOptions(ctx, "ensure-pod-builder-tests", "", logger)
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	if otelShutdown != nil {
		_ = otelShutdown(context.Background())
	}
})

var _ = Describe("PodFactory for drivers-builder with Instructions", func() {
	var (
		ctx          context.Context
		clusterImage string
		builderImage string
	)

	BeforeEach(func() {
		ctx = context.Background()
		clusterImage = "quay.io/weka.io/weka-in-container:4.5.0.100"
		builderImage = "quay.io/weka.io/weka-drivers-build-images:builder-ubuntu22"
	})

	It("should add init containers and TARGET_IMAGE_NAME when Instructions is set", func() {
		payloadBytes, _ := json.Marshal(map[string]string{
			"targetImage": clusterImage,
			"cliImage":    builderImage,
		})

		container := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-builder",
				Namespace: "default",
				UID:       "test-uid",
			},
			Spec: weka.WekaContainerSpec{
				Image:     clusterImage,
				Mode:      weka.WekaContainerModeDriversBuilder,
				CpuPolicy: weka.CpuPolicyShared,
				Instructions: &weka.Instructions{
					Type:    weka.InstructionCopyWekaFilesToDriverLoader,
					Payload: string(payloadBytes),
				},
			},
		}

		nodeInfo := &discovery.DiscoveryNodeInfo{}
		factory := resources.NewPodFactory(container, nodeInfo, nil)

		podImage := builderImage
		pod, err := factory.Create(ctx, &podImage)
		Expect(err).NotTo(HaveOccurred())
		Expect(pod).NotTo(BeNil())

		// Verify init containers are created
		initContainerNames := make([]string, len(pod.Spec.InitContainers))
		for i, ic := range pod.Spec.InitContainers {
			initContainerNames[i] = ic.Name
		}
		Expect(initContainerNames).To(ContainElement("copy-cli"))
		Expect(initContainerNames).To(ContainElement("copy-weka-version"))

		// Verify copy-cli uses the builder (cli) image
		for _, ic := range pod.Spec.InitContainers {
			if ic.Name == "copy-cli" {
				Expect(ic.Image).To(Equal(builderImage))
			}
			if ic.Name == "copy-weka-version" {
				Expect(ic.Image).To(Equal(clusterImage))
			}
		}

		// Verify TARGET_IMAGE_NAME env var is set to cluster image
		var targetImageNameValue string
		for _, env := range pod.Spec.Containers[0].Env {
			if env.Name == "TARGET_IMAGE_NAME" {
				targetImageNameValue = env.Value
				break
			}
		}
		Expect(targetImageNameValue).To(Equal(clusterImage),
			"TARGET_IMAGE_NAME should be the target (cluster) image")
	})

	It("should not add init containers or TARGET_IMAGE_NAME when Instructions is nil", func() {
		container := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-builder",
				Namespace: "default",
				UID:       "test-uid",
			},
			Spec: weka.WekaContainerSpec{
				Image:     clusterImage,
				Mode:      weka.WekaContainerModeDriversBuilder,
				CpuPolicy: weka.CpuPolicyShared,
			},
		}

		nodeInfo := &discovery.DiscoveryNodeInfo{}
		factory := resources.NewPodFactory(container, nodeInfo, nil)

		podImage := clusterImage
		pod, err := factory.Create(ctx, &podImage)
		Expect(err).NotTo(HaveOccurred())
		Expect(pod).NotTo(BeNil())

		// No init containers for copy operations
		for _, ic := range pod.Spec.InitContainers {
			Expect(ic.Name).NotTo(Equal("copy-cli"))
			Expect(ic.Name).NotTo(Equal("copy-weka-version"))
		}

		// No TARGET_IMAGE_NAME env var
		for _, env := range pod.Spec.Containers[0].Env {
			Expect(env.Name).NotTo(Equal("TARGET_IMAGE_NAME"))
		}
	})
})
