package operations

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-logr/zerologr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rs/zerolog"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/drivers"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

var otelShutdown func(context.Context) error

func TestLoadDrivers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "LoadDrivers Suite")
}

var _ = BeforeSuite(func() {
	ctx := context.Background()
	writer := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.TimeOnly}
	zeroLogger := zerolog.New(writer).Level(zerolog.DebugLevel).With().Timestamp().Logger()
	logger := zerologr.New(&zeroLogger)

	var err error
	otelShutdown, err = instrumentation.SetupOTelSDK(ctx, "load-drivers-tests", "", logger)
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	if otelShutdown != nil {
		_ = otelShutdown(context.Background())
	}
})

var _ = Describe("LoadDrivers CreateContainer", func() {
	var (
		ctx          context.Context
		scheme       *runtime.Scheme
		clusterImage string
		node         *corev1.Node
	)

	BeforeEach(func() {
		ctx = context.Background()
		clusterImage = "quay.io/weka.io/weka-in-container:4.5.0.100"

		scheme = runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(weka.AddToScheme(scheme)).To(Succeed())

		node = &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-node",
				UID:  "test-node-uid",
			},
			Status: corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					OSImage: "Ubuntu 22.04.5 LTS",
					BootID:  "test-boot-id",
				},
			},
		}
	})

	It("should set DriversLoaderImage to cluster image and no Instructions when WekaGetCopyLocalDriverFiles is true", func() {
		// Pre-populate the feature flags cache with the flag enabled
		flags := &domain.FeatureFlags{
			WekaGetCopyLocalDriverFiles: true,
		}
		err := services.SetFeatureFlags(ctx, clusterImage, flags)
		Expect(err).NotTo(HaveOccurred())

		// Verify GetLoaderImageForNode returns the cluster image
		loaderImage := drivers.GetLoaderImageForNode(ctx, node, clusterImage)
		Expect(loaderImage).To(Equal(clusterImage),
			"When WekaGetCopyLocalDriverFiles is true, loader image should be the cluster image")

		// Create a fake client to verify the container spec
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

		// Create the LoadDrivers operation
		loadDrivers := &LoadDrivers{
			client:    fakeClient,
			scheme:    scheme,
			node:      node,
			namespace: "default",
			containerDetails: weka.WekaOwnerDetails{
				Image: clusterImage,
			},
		}

		// Execute CreateContainer
		err = loadDrivers.CreateContainer(ctx)
		Expect(err).NotTo(HaveOccurred())

		// Verify the created container has the correct DriversLoaderImage
		Expect(loadDrivers.container).NotTo(BeNil())
		Expect(loadDrivers.container.Spec.Image).To(Equal(clusterImage),
			"Spec.Image should always be the cluster image")
		Expect(loadDrivers.container.Spec.DriversLoaderImage).To(Equal(clusterImage),
			"DriversLoaderImage should be the cluster image when WekaGetCopyLocalDriverFiles is true")
		// No Instructions needed when images are the same
		Expect(loadDrivers.container.Spec.Instructions).To(BeNil(),
			"Instructions should be nil when loader image equals cluster image")
	})

	It("should set DriversLoaderImage to builder image and set Instructions when images differ", func() {
		// Use a different image that is NOT in the feature flags cache
		uncachedImage := "quay.io/weka.io/weka-in-container:4.4.0.50-uncached-for-test"

		// Verify GetLoaderImageForNode returns the builder image (not the cluster image)
		loaderImage := drivers.GetLoaderImageForNode(ctx, node, uncachedImage)
		expectedBuilderImage := "quay.io/weka.io/weka-drivers-build-images:builder-ubuntu22"
		Expect(loaderImage).To(Equal(expectedBuilderImage),
			"When feature flags are not cached, loader image should be the builder image")

		// Create a fake client
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

		// Create the LoadDrivers operation
		loadDrivers := &LoadDrivers{
			client:    fakeClient,
			scheme:    scheme,
			node:      node,
			namespace: "default",
			containerDetails: weka.WekaOwnerDetails{
				Image: uncachedImage,
			},
		}

		// Execute CreateContainer
		err := loadDrivers.CreateContainer(ctx)
		Expect(err).NotTo(HaveOccurred())

		// Verify the created container specs
		Expect(loadDrivers.container).NotTo(BeNil())
		Expect(loadDrivers.container.Spec.Image).To(Equal(uncachedImage),
			"Spec.Image should always be the cluster image")
		Expect(loadDrivers.container.Spec.DriversLoaderImage).To(Equal(expectedBuilderImage),
			"DriversLoaderImage should be the builder image when feature flag is not set")

		// Instructions should be set to copy weka files from cluster image
		Expect(loadDrivers.container.Spec.Instructions).NotTo(BeNil(),
			"Instructions should be set when loader image differs from cluster image")
		Expect(loadDrivers.container.Spec.Instructions.Type).To(Equal(weka.InstructionCopyWekaFilesToDriverLoader),
			"Instructions.Type should be InstructionCopyWekaFilesToDriverLoader")
		Expect(loadDrivers.container.Spec.Instructions.Payload).To(Equal(uncachedImage),
			"Instructions.Payload should be the cluster image to copy weka files from")
	})
})

var _ = Describe("PodFactory IMAGE_NAME for drivers-loader", func() {
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

	It("should set IMAGE_NAME to cluster image when DriversLoaderImage equals cluster image", func() {
		container := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-drivers-loader",
				Namespace: "default",
				UID:       "test-uid",
			},
			Spec: weka.WekaContainerSpec{
				Image:              clusterImage,
				Mode:               weka.WekaContainerModeDriversLoader,
				DriversLoaderImage: clusterImage, // Same as cluster image
				CpuPolicy:          weka.CpuPolicyShared,
			},
		}

		nodeInfo := &discovery.DiscoveryNodeInfo{}
		factory := resources.NewPodFactory(container, nodeInfo)

		// Pass cluster image as the pod image (simulating ensurePod behavior)
		podImage := clusterImage
		pod, err := factory.Create(ctx, &podImage)
		Expect(err).NotTo(HaveOccurred())
		Expect(pod).NotTo(BeNil())

		// Find IMAGE_NAME env var
		var imageNameValue string
		for _, env := range pod.Spec.Containers[0].Env {
			if env.Name == "IMAGE_NAME" {
				imageNameValue = env.Value
				break
			}
		}

		Expect(imageNameValue).To(Equal(clusterImage),
			"IMAGE_NAME should be the cluster image when pod uses cluster image")
		Expect(pod.Spec.Containers[0].Image).To(Equal(clusterImage),
			"Pod image should be the cluster image")
	})

	It("should set IMAGE_NAME to builder image when DriversLoaderImage differs from cluster image", func() {
		container := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-drivers-loader",
				Namespace: "default",
				UID:       "test-uid",
			},
			Spec: weka.WekaContainerSpec{
				Image:              clusterImage,
				Mode:               weka.WekaContainerModeDriversLoader,
				DriversLoaderImage: builderImage, // Different from cluster image
				CpuPolicy:          weka.CpuPolicyShared,
				Instructions: &weka.Instructions{
					Type:    weka.InstructionCopyWekaFilesToDriverLoader,
					Payload: clusterImage,
				},
			},
		}

		nodeInfo := &discovery.DiscoveryNodeInfo{}
		factory := resources.NewPodFactory(container, nodeInfo)

		// Pass builder image as the pod image (simulating ensurePod behavior when images differ)
		podImage := builderImage
		pod, err := factory.Create(ctx, &podImage)
		Expect(err).NotTo(HaveOccurred())
		Expect(pod).NotTo(BeNil())

		// Find IMAGE_NAME env var
		var imageNameValue string
		for _, env := range pod.Spec.Containers[0].Env {
			if env.Name == "IMAGE_NAME" {
				imageNameValue = env.Value
				break
			}
		}

		Expect(imageNameValue).To(Equal(builderImage),
			"IMAGE_NAME should be the builder image when pod uses builder image")
		Expect(pod.Spec.Containers[0].Image).To(Equal(builderImage),
			"Pod image should be the builder image")

		// Verify CLUSTER_IMAGE_NAME is set to the cluster image
		var clusterImageNameValue string
		for _, env := range pod.Spec.Containers[0].Env {
			if env.Name == "CLUSTER_IMAGE_NAME" {
				clusterImageNameValue = env.Value
				break
			}
		}

		Expect(clusterImageNameValue).To(Equal(clusterImage),
			"CLUSTER_IMAGE_NAME should always be the cluster image")
	})
})
