package wekacontainer

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/weka/go-weka-observability/instrumentation"
	obslogger "github.com/weka/go-weka-observability/logger"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/controllers/resources"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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

// Covers the reclaim branch in ensurePod: a drive/compute backend that joined the cluster
// (ClusterContainerID set) but whose pod was released on a cordoned (unschedulable) node is handed
// to the deletion flow instead of looping forever on the "node is unschedulable" WaitError (which
// would strand it in active/Error and keep the node's lifecycle hold from releasing the instance).
// Anything that never joined, still has a pod, or is not a backend keeps waiting.
var _ = Describe("ensurePod reclaim of a released backend on a cordoned node", func() {
	var scheme *runtime.Scheme

	BeforeEach(func() {
		scheme = runtime.NewScheme()
		Expect(v1.AddToScheme(scheme)).To(Succeed())
		Expect(weka.AddToScheme(scheme)).To(Succeed())
	})

	newContainer := func(mode string, joined bool) *weka.WekaContainer {
		c := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default"},
			Spec:       weka.WekaContainerSpec{Mode: mode, State: weka.ContainerStateActive},
		}
		if joined {
			id := 3
			c.Status.ClusterContainerID = &id
		}
		return c
	}

	// stoppedAgo stamps TimestampStopAttempt as handlePodTermination would when it first observes
	// the pod Terminating, so the reclaim debounce can be exercised.
	stoppedAgo := func(c *weka.WekaContainer, d time.Duration) *weka.WekaContainer {
		c.Status.Timestamps = map[string]metav1.Time{
			string(weka.TimestampStopAttempt): {Time: time.Now().Add(-d)},
		}
		return c
	}

	cordoned := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}, Spec: v1.NodeSpec{Unschedulable: true}}
	presentPod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default"}}

	run := func(c *weka.WekaContainer, pod *v1.Pod) error {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(c).Build()
		r := &containerReconcilerLoop{Client: fakeClient, container: c, pod: pod, node: cordoned}
		return r.ensurePod(context.Background())
	}

	It("moves a joined drive whose pod has been released past the grace into deletion", func() {
		c := stoppedAgo(newContainer(weka.WekaContainerModeDrive, true), time.Minute)
		Expect(run(c, nil)).To(Succeed())
		Expect(c.Spec.State).To(Equal(weka.ContainerStateDeleting))
	})

	It("moves a joined compute whose pod has been released past the grace into deletion", func() {
		c := stoppedAgo(newContainer(weka.WekaContainerModeCompute, true), time.Minute)
		Expect(run(c, nil)).To(Succeed())
		Expect(c.Spec.State).To(Equal(weka.ContainerStateDeleting))
	})

	It("holds (does not delete) within the debounce grace", func() {
		c := stoppedAgo(newContainer(weka.WekaContainerModeDrive, true), 5*time.Second)
		Expect(run(c, nil)).To(HaveOccurred())
		Expect(c.Spec.State).To(Equal(weka.ContainerStateActive))
	})

	It("holds (does not delete) when the pod was never observed terminating", func() {
		c := newContainer(weka.WekaContainerModeDrive, true) // no TimestampStopAttempt
		Expect(run(c, nil)).To(HaveOccurred())
		Expect(c.Spec.State).To(Equal(weka.ContainerStateActive))
	})

	It("holds (does not delete) a backend that never joined the cluster", func() {
		c := stoppedAgo(newContainer(weka.WekaContainerModeDrive, false), time.Minute)
		Expect(run(c, nil)).To(HaveOccurred())
		Expect(c.Spec.State).To(Equal(weka.ContainerStateActive))
	})

	It("holds (does not delete) while the pod is still present", func() {
		c := stoppedAgo(newContainer(weka.WekaContainerModeDrive, true), time.Minute)
		Expect(run(c, presentPod)).To(HaveOccurred())
		Expect(c.Spec.State).To(Equal(weka.ContainerStateActive))
	})

	It("holds (does not delete) a non-backend container", func() {
		c := stoppedAgo(newContainer(weka.WekaContainerModeDiscovery, true), time.Minute)
		Expect(run(c, nil)).To(HaveOccurred())
		Expect(c.Spec.State).To(Equal(weka.ContainerStateActive))
	})
})
