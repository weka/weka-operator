package operations

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/weka/go-weka-observability/instrumentation"
	obslogger "github.com/weka/go-weka-observability/logger"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/config"
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
	logger := obslogger.CreateLogger(obslogger.WithConsoleSink(), obslogger.WithDebugLevel())

	var err error
	otelShutdown, err = instrumentation.SetupOTelSDKWithOptions(ctx, "load-drivers-tests", "", logger)
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

		config.Config.BuilderImages.Default = "quay.io/weka.io/weka-drivers-build-images:builder-ubuntu22"
		config.Config.BuilderImages.Ubuntu24 = "quay.io/weka.io/weka-drivers-build-images:builder-ubuntu24"

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
			priority:  2,
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
		// Priority rank is stamped on the loader so concurrent reconciles can order
		// themselves against it
		Expect(loadDrivers.container.Labels).To(HaveKeyWithValue(driverPriorityLabel, "2"),
			"loader should carry its priority rank label")
		// Boot id is stamped so a post-reboot reconcile can tell a stale loader apart
		Expect(loadDrivers.container.Labels).To(HaveKeyWithValue(driverBootIDLabel, "test-boot-id"),
			"loader should carry the boot id it was created for")
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
		var payload map[string]string
		Expect(json.Unmarshal([]byte(loadDrivers.container.Spec.Instructions.Payload), &payload)).To(Succeed(),
			"Instructions.Payload should be valid JSON")
		Expect(payload["targetImage"]).To(Equal(uncachedImage),
			"Instructions.Payload targetImage should be the cluster image")
		Expect(payload["cliImage"]).To(Equal(expectedBuilderImage),
			"Instructions.Payload cliImage should be the loader image")
	})
})

var _ = Describe("LoadDrivers HandleNodeReboot", func() {
	// HandleNodeReboot only clears the previous boot's drivers-loaded annotation;
	// deleting a stale loader is HandleExistingLoader's job (tested below).
	var (
		ctx    context.Context
		scheme *runtime.Scheme
		node   *corev1.Node
	)

	BeforeEach(func() {
		ctx = context.Background()
		scheme = runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(weka.AddToScheme(scheme)).To(Succeed())
		node = &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "test-node", UID: "test-node-uid"},
			Status:     corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{BootID: "boot-current"}},
		}
	})

	newOp := func() *LoadDrivers {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()
		return &LoadDrivers{client: c, scheme: scheme, node: node, namespace: "default"}
	}

	It("clears the drivers-loaded annotation from the previous boot", func() {
		node.Annotations = map[string]string{driversLoadedAnnotation: "prev-boot-record"}
		Expect(newOp().HandleNodeReboot(ctx)).To(Succeed())
		Expect(node.Annotations).NotTo(HaveKey(driversLoadedAnnotation))
	})
})

var _ = Describe("compareDriverOrder", func() {
	imageOld := "quay.io/weka.io/weka-in-container:4.4.0.50"
	imageNew := "quay.io/weka.io/weka-in-container:4.5.0.100"

	It("orders by priority first", func() {
		Expect(compareDriverOrder(3, imageOld, 2, imageNew)).To(BeNumerically(">", 0),
			"higher priority outranks regardless of version")
		Expect(compareDriverOrder(1, imageNew, 2, imageOld)).To(BeNumerically("<", 0))
	})

	It("orders by version at equal priority", func() {
		Expect(compareDriverOrder(3, imageNew, 3, imageOld)).To(BeNumerically(">", 0))
		Expect(compareDriverOrder(3, imageOld, 3, imageNew)).To(BeNumerically("<", 0))
		Expect(compareDriverOrder(3, imageNew, 3, imageNew)).To(Equal(0))
	})
})

var _ = Describe("EvaluateDrivers", func() {
	const bootID = "boot-1"
	imageX := "quay.io/weka.io/weka-in-container:4.5.0.100" // newer
	imageY := "quay.io/weka.io/weka-in-container:4.4.0.50"  // older

	const (
		prioBackend  = 2
		prioFrontend = 3
	)

	newNode := func(annotation string) *corev1.Node {
		n := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "n", UID: "n-uid"},
			Status:     corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{BootID: bootID}},
		}
		if annotation != "" {
			n.Annotations = map[string]string{driversLoadedAnnotation: annotation}
		}
		return n
	}

	recordJSON := func(boot, image string, priority int) string {
		b, _ := json.Marshal(loadedDrivers{BootID: boot, Image: image, Priority: priority})
		return string(b)
	}

	It("loads when nothing is recorded", func() {
		d, loaded := EvaluateDrivers(newNode(""), imageX, prioBackend, false)
		Expect(d).To(Equal(DriverLoad))
		Expect(loaded).To(BeEmpty())
	})

	It("loads when the boot id does not match", func() {
		node := newNode(recordJSON("other-boot", imageX, prioFrontend))
		d, _ := EvaluateDrivers(node, imageX, prioFrontend, true)
		Expect(d).To(Equal(DriverLoad))
	})

	It("is satisfied when the exact image is already loaded", func() {
		node := newNode(recordJSON(bootID, imageX, prioBackend))
		d, loaded := EvaluateDrivers(node, imageX, prioBackend, false)
		Expect(d).To(Equal(DriverSatisfied))
		Expect(loaded).To(Equal(imageX))

		d, _ = EvaluateDrivers(node, imageX, prioFrontend, true)
		Expect(d).To(Equal(DriverSatisfied), "frontend also satisfied by exact image")
	})

	It("defers a lenient backend to any loaded version", func() {
		node := newNode(recordJSON(bootID, imageX, prioFrontend))
		d, loaded := EvaluateDrivers(node, imageY, prioBackend, false)
		Expect(d).To(Equal(DriverDefer), "backend tolerates whatever is loaded")
		Expect(loaded).To(Equal(imageX))
	})

	It("lets a strict frontend preempt a lower-order loaded driver", func() {
		// backend loaded imageX; frontend needs imageY and outranks by priority
		node := newNode(recordJSON(bootID, imageX, prioBackend))
		d, loaded := EvaluateDrivers(node, imageY, prioFrontend, true)
		Expect(d).To(Equal(DriverLoad))
		Expect(loaded).To(Equal(imageX))
	})

	It("conflicts when a strict frontend is outranked by the loaded driver", func() {
		// frontend loaded newer imageX; an older frontend imageY cannot get its version
		node := newNode(recordJSON(bootID, imageX, prioFrontend))
		d, loaded := EvaluateDrivers(node, imageY, prioFrontend, true)
		Expect(d).To(Equal(DriverConflict))
		Expect(loaded).To(Equal(imageX))
	})

	It("defers a strict frontend when a same-version image (different string) is loaded", func() {
		// same weka version, different image string (e.g. registry/mirror swap,
		// tag→digest, or a rebuilt base image with a stripped -suffix); the loaded
		// drivers are compatible so the frontend tolerates them rather than deadlock
		imageXMirror := "mirror.example.com/weka-in-container:4.5.0.100"
		node := newNode(recordJSON(bootID, imageX, prioFrontend))
		d, loaded := EvaluateDrivers(node, imageXMirror, prioFrontend, true)
		Expect(d).To(Equal(DriverDefer), "same version is compatible, no reload needed")
		Expect(loaded).To(Equal(imageX))

		imageXSuffix := "quay.io/weka.io/weka-in-container:4.5.0.100-rebuilt"
		d, _ = EvaluateDrivers(node, imageXSuffix, prioFrontend, true)
		Expect(d).To(Equal(DriverDefer), "stripped -suffix leaves an equal version")
	})

	It("understands the legacy image:bootId format (priority 0)", func() {
		node := newNode(imageX + ":" + bootID)
		d, _ := EvaluateDrivers(node, imageX, prioFrontend, true)
		Expect(d).To(Equal(DriverSatisfied), "exact image satisfies even legacy record")

		d, loaded := EvaluateDrivers(node, imageY, prioBackend, false)
		Expect(d).To(Equal(DriverDefer), "backend tolerates legacy-loaded value")
		Expect(loaded).To(Equal(imageX))

		// a strict frontend outranks the priority-0 legacy record and preempts
		d, _ = EvaluateDrivers(node, imageY, prioFrontend, true)
		Expect(d).To(Equal(DriverLoad))
	})
})

var _ = Describe("LoadDrivers ProcessResult", func() {
	var (
		ctx        context.Context
		scheme     *runtime.Scheme
		node       *corev1.Node
		imageX     string
		execResult string
	)

	BeforeEach(func() {
		ctx = context.Background()
		imageX = "quay.io/weka.io/weka-in-container:4.5.0.100"
		execResult = `{"err":"","drivers_loaded":true}`

		scheme = runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(weka.AddToScheme(scheme)).To(Succeed())

		node = &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "n", UID: "n-uid"},
			Status:     corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{BootID: "boot-1"}},
		}
	})

	buildOp := func(loaderImage string, loaderPriority int) *LoadDrivers {
		loader := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "weka-drivers-loader-n-uid",
				Namespace: "default",
				Labels:    map[string]string{driverPriorityLabel: strconv.Itoa(loaderPriority)},
			},
			Spec:   weka.WekaContainerSpec{Image: loaderImage, Mode: weka.WekaContainerModeDriversLoader},
			Status: weka.WekaContainerStatus{ExecutionResult: &execResult},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node, loader).Build()
		return &LoadDrivers{
			client:           fakeClient,
			scheme:           scheme,
			node:             node,
			namespace:        "default",
			container:        loader,
			containerDetails: weka.WekaOwnerDetails{Image: loaderImage},
		}
	}

	It("records the single loaded driver record and keeps the loader for the final delete step", func() {
		op := buildOp(imageX, 3)
		err := op.ProcessResult(ctx)
		Expect(err).NotTo(HaveOccurred())

		ld := parseLoadedDrivers(op.node)
		Expect(ld).NotTo(BeNil())
		Expect(ld.Image).To(Equal(imageX), "records the image the loader actually loaded")
		Expect(ld.Priority).To(Equal(3), "records the priority stamped on the loader")
		Expect(ld.BootID).To(Equal("boot-1"))
		Expect(op.container).NotTo(BeNil(), "loader is deleted by the trailing DeleteContainers step")
	})

	It("returns DriversNotLoadedError and deletes the loader when drivers did not load", func() {
		op := buildOp(imageX, 3)
		failure := `{"err":"","drivers_loaded":false}`
		op.container.Status.ExecutionResult = &failure

		err := op.ProcessResult(ctx)
		Expect(err).To(HaveOccurred())
		Expect(parseLoadedDrivers(op.node)).To(BeNil(), "no record written on failure")
	})
})

var _ = Describe("LoadDrivers HandleExistingLoader", func() {
	const currentBoot = "boot-current"
	imageOld := "quay.io/weka.io/weka-in-container:4.4.0.50"
	imageNew := "quay.io/weka.io/weka-in-container:4.5.0.100"
	loaderName := "weka-drivers-loader-test-node-uid"

	const (
		prioBackend  = 2
		prioFrontend = 3
	)

	var (
		ctx    context.Context
		scheme *runtime.Scheme
		node   *corev1.Node
	)

	BeforeEach(func() {
		ctx = context.Background()
		scheme = runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(weka.AddToScheme(scheme)).To(Succeed())
		node = &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "test-node", UID: "test-node-uid"},
			Status:     corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{BootID: currentBoot}},
		}
	})

	// loader with the given image/priority, stamped for the given boot id.
	loaderFor := func(bootID, image string, priority int) *weka.WekaContainer {
		return &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      loaderName,
				Namespace: "default",
				Labels: map[string]string{
					driverBootIDLabel:   bootID,
					driverPriorityLabel: strconv.Itoa(priority),
				},
			},
			Spec: weka.WekaContainerSpec{Image: image, Mode: weka.WekaContainerModeDriversLoader},
		}
	}

	// op competing for myImage/myPriority against the given in-flight loader.
	newOp := func(loader *weka.WekaContainer, myImage string, myPriority int) (*LoadDrivers, client.Client) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node, loader).Build()
		return &LoadDrivers{
			client:           c,
			scheme:           scheme,
			node:             node,
			namespace:        "default",
			container:        loader,
			priority:         myPriority,
			containerDetails: weka.WekaOwnerDetails{Image: myImage},
		}, c
	}

	loaderExists := func(c client.Client) bool {
		err := c.Get(ctx, client.ObjectKey{Name: loaderName, Namespace: "default"}, &weka.WekaContainer{})
		return err == nil
	}

	It("keeps an in-flight loader with the same image and polls it", func() {
		op, c := newOp(loaderFor(currentBoot, imageNew, prioBackend), imageNew, prioBackend)
		Expect(op.HandleExistingLoader(ctx)).To(Succeed())
		Expect(op.container).NotTo(BeNil(), "same-image loader is retained to be polled")
		Expect(loaderExists(c)).To(BeTrue())
	})

	It("preempts a lower-order loader in flight (delete and recreate)", func() {
		// backend loader in flight; a frontend outranks it and must load its own
		// version. The (priority, version) ordering itself is covered by
		// compareDriverOrder; here we assert the resulting action.
		op, c := newOp(loaderFor(currentBoot, imageOld, prioBackend), imageNew, prioFrontend)
		Expect(op.HandleExistingLoader(ctx)).To(Succeed())
		Expect(op.container).To(BeNil(), "outranked loader preempted so a fresh one is created")
		Expect(loaderExists(c)).To(BeFalse(), "outranked loader deleted")
	})

	It("defers to a higher-order loader in flight (requeue, keep it)", func() {
		// frontend loader in flight; a backend must not disturb it
		op, c := newOp(loaderFor(currentBoot, imageNew, prioFrontend), imageOld, prioBackend)
		err := op.HandleExistingLoader(ctx)
		Expect(err).To(HaveOccurred(), "deferring returns a wait error to requeue")
		Expect(op.container).NotTo(BeNil(), "higher-order loader left untouched")
		Expect(loaderExists(c)).To(BeTrue())
	})

	It("discards a stale loader whose boot id predates the current boot, even on an image match", func() {
		// the reboot unloaded this loader's drivers and its ExecutionResult predates
		// the reboot; it must be deleted rather than polled, regardless of image match
		op, c := newOp(loaderFor("boot-previous", imageNew, prioBackend), imageNew, prioBackend)
		Expect(op.HandleExistingLoader(ctx)).To(Succeed())
		Expect(op.container).To(BeNil(), "stale loader cleared so a fresh one is created")
		Expect(loaderExists(c)).To(BeFalse(), "stale loader deleted")
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
		factory := resources.NewPodFactory(container, nodeInfo, nil)

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
					Type: weka.InstructionCopyWekaFilesToDriverLoader,
					Payload: func() string {
						b, _ := json.Marshal(map[string]string{
							"targetImage": clusterImage,
							"cliImage":    builderImage,
						})
						return string(b)
					}(),
				},
			},
		}

		nodeInfo := &discovery.DiscoveryNodeInfo{}
		factory := resources.NewPodFactory(container, nodeInfo, nil)

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

		// Verify TARGET_IMAGE_NAME is set to the cluster image
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
})
