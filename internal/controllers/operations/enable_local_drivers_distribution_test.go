package operations

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/weka/go-steps-engine/throttling"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services"
)

var _ = Describe("sanitizeOsImageForLabel", func() {
	It("should convert OS image string to a valid Kubernetes label value", func() {
		Expect(sanitizeOsImageForLabel("Ubuntu 22.04.5 LTS")).To(Equal("ubuntu-22.04.5-lts"))
		Expect(sanitizeOsImageForLabel("Red Hat Enterprise Linux 9.2")).To(Equal("red-hat-enterprise-linux-9.2"))
		Expect(sanitizeOsImageForLabel("simple")).To(Equal("simple"))
		Expect(sanitizeOsImageForLabel("")).To(Equal(""))
	})

	It("should strip parentheses and other invalid label characters", func() {
		Expect(sanitizeOsImageForLabel("Red Hat Enterprise Linux CoreOS 9.6.20251119-0 (Plow)")).To(Equal("red-hat-enterprise-linux-coreos-9.6.20251119-0-plow"))
		Expect(sanitizeOsImageForLabel("24.04.4 LTS (Noble Numbat)")).To(Equal("24.04.4-lts-noble-numbat"))
		Expect(sanitizeOsImageForLabel("123Abc$%#$%#$%Def-~-")).To(Equal("123abcdef"))
	})
})

var _ = Describe("nodeAttributes StringKey", func() {
	It("should generate a consistent string key from node attributes", func() {
		na := nodeAttributes{
			kernelVersion: "5.15.0-100-generic",
			architecture:  "amd64",
			osImage:       "Ubuntu 22.04.5 LTS",
			nodeSelector:  map[string]string{"zone": "us-east-1a", "env": "prod"},
		}

		key := na.StringKey()
		Expect(key).To(ContainSubstring("kernel=5.15.0-100-generic"))
		Expect(key).To(ContainSubstring("arch=amd64"))
		Expect(key).To(ContainSubstring("osImage=ubuntu-22.04.5-lts"))
		// Selectors should be sorted by key
		Expect(key).To(ContainSubstring("selector=env:prod,zone:us-east-1a"))

		// Same attributes should produce the same key
		Expect(na.StringKey()).To(Equal(key))
	})

	It("should produce different keys for different node selectors", func() {
		na1 := nodeAttributes{
			kernelVersion: "5.15.0",
			architecture:  "amd64",
			osImage:       "Ubuntu",
			nodeSelector:  map[string]string{"zone": "a"},
		}
		na2 := nodeAttributes{
			kernelVersion: "5.15.0",
			architecture:  "amd64",
			osImage:       "Ubuntu",
			nodeSelector:  map[string]string{"zone": "b"},
		}
		Expect(na1.StringKey()).NotTo(Equal(na2.StringKey()))
	})
})

var _ = Describe("EnsureBuilderContainers", func() {
	var (
		ctx                context.Context
		scheme             *runtime.Scheme
		policy             *weka.WekaPolicy
		namespace          string
		image              string
		kernel             string
		arch               string
		osImage            string
		serviceAccountName string
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespace = "default"
		image = "quay.io/weka.io/weka-in-container:4.5.0.100"
		kernel = "5.15.0-100-generic"
		arch = "amd64"
		osImage = "Ubuntu 22.04.5 LTS"
		serviceAccountName = "weka-runtime"

		scheme = runtime.NewScheme()
		Expect(weka.AddToScheme(scheme)).To(Succeed())

		policy = &weka.WekaPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-policy",
				Namespace: namespace,
				UID:       "test-policy-uid",
			},
		}
	})

	It("should create two builder containers with correct new names for two ensureImages", func() {
		image1 := "quay.io/weka.io/weka-in-container:4.5.0.100"
		image2 := "quay.io/weka.io/weka-in-container:4.6.0.50"

		// Pre-create a dist container so getDistContainerName finds it
		distContainer := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-policy-dist-abc",
				Namespace: namespace,
				Labels: map[string]string{
					PolicyNameLabelKey: policy.GetName(),
					"app":              "test-policy" + DriverDistContainerSuffix,
					"weka.io/mode":     weka.WekaContainerModeDriversDist,
				},
			},
			Spec: weka.WekaContainerSpec{
				Mode: weka.WekaContainerModeDriversDist,
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(distContainer).
			Build()

		op := &EnsureDistServiceOperation{
			client: fakeClient,
			scheme: scheme,
			policy: policy,
			payload: &weka.DriverDistPayload{
				EnsureImages: []string{image1, image2},
			},
			containerDetails: weka.WekaOwnerDetails{
				Image:              image,
				ServiceAccountName: serviceAccountName,
			},
			discoveredImages: map[string]bool{image1: true, image2: true},
			builderNames:     map[string]string{},
			targetKernelArchs: map[string]nodeAttributes{
				"key1": {
					kernelVersion: kernel,
					architecture:  arch,
					osImage:       osImage,
				},
			},
			discoveredNodesAttr: make(map[string]nodeAttributes),
		}

		err := op.EnsureBuilderContainers(ctx)
		Expect(err).NotTo(HaveOccurred())

		// Format: {policyName}-builder-{hashFNV(image+arch)}-{normalizedOs}-{kernelNorm}-{random}
		suffix := "-ubuntu-22-04-5-15-0-1" // GenerateName keeps the first 58 characters of the base
		prefix1 := "test-policy-builder-" + hashFNV(image1+arch) + suffix
		prefix2 := "test-policy-builder-" + hashFNV(image2+arch) + suffix

		var allContainers weka.WekaContainerList
		err = fakeClient.List(ctx, &allContainers, &client.ListOptions{Namespace: namespace})
		Expect(err).NotTo(HaveOccurred())

		imageByPrefix := map[string]string{prefix1: image1, prefix2: image2}
		found := map[string]bool{}
		for _, wc := range allContainers.Items {
			if wc.Spec.Mode != weka.WekaContainerModeDriversBuilder {
				continue
			}
			Expect(wc.Spec.ServiceAccountName).To(Equal(serviceAccountName))
			for prefix, image := range imageByPrefix {
				if strings.HasPrefix(wc.Name, prefix) {
					Expect(wc.Spec.Image).To(Equal(image))
					found[prefix] = true
				}
			}
		}
		Expect(found).To(HaveLen(2), "one builder per ensureImage, named by its target")
	})

	It("should reuse an existing old-name builder container instead of creating a new one", func() {
		// Build the old-format name to pre-create it
		op := &EnsureDistServiceOperation{
			policy: policy,
		}
		oldName := op.getOldBuilderContainerName(image, kernel, arch)

		// Pre-create a WekaContainer with the old name
		existingOldBuilder := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      oldName,
				Namespace: namespace,
			},
			Spec: weka.WekaContainerSpec{
				Image: image,
				Mode:  weka.WekaContainerModeDriversBuilder,
			},
		}

		// Also pre-create a dist container so getDistContainerName finds it
		distContainer := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-policy-dist-abc",
				Namespace: namespace,
				Labels: map[string]string{
					PolicyNameLabelKey: policy.GetName(),
					"app":              "test-policy" + DriverDistContainerSuffix,
					"weka.io/mode":     weka.WekaContainerModeDriversDist,
				},
			},
			Spec: weka.WekaContainerSpec{
				Mode: weka.WekaContainerModeDriversDist,
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingOldBuilder, distContainer).
			Build()

		op = &EnsureDistServiceOperation{
			client:  fakeClient,
			scheme:  scheme,
			policy:  policy,
			payload: &weka.DriverDistPayload{},
			containerDetails: weka.WekaOwnerDetails{
				Image: image,
			},
			discoveredImages: map[string]bool{image: true},
			builderNames:     map[string]string{},
			targetKernelArchs: map[string]nodeAttributes{
				"key1": {
					kernelVersion: kernel,
					architecture:  arch,
					osImage:       osImage,
				},
			},
			discoveredNodesAttr: make(map[string]nodeAttributes),
		}

		err := op.EnsureBuilderContainers(ctx)
		Expect(err).NotTo(HaveOccurred())

		// Verify the old-name container still exists and was updated
		var oldWc weka.WekaContainer
		err = fakeClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: oldName}, &oldWc)
		Expect(err).NotTo(HaveOccurred(), "Old-name builder container should still exist")
		Expect(oldWc.Spec.Mode).To(Equal(weka.WekaContainerModeDriversBuilder))

		// Verify no new-name container was created — list all builders and check count
		var allContainers weka.WekaContainerList
		err = fakeClient.List(ctx, &allContainers, &client.ListOptions{Namespace: namespace})
		Expect(err).NotTo(HaveOccurred())

		builderCount := 0
		for _, wc := range allContainers.Items {
			if wc.Spec.Mode == weka.WekaContainerModeDriversBuilder {
				builderCount++
				// The only builder should be the old-name one
				Expect(wc.Name).To(Equal(oldName),
					"Only the old-name builder container should exist, got: "+wc.Name)
			}
		}
		Expect(builderCount).To(Equal(1), "Exactly one builder container should exist")
	})
})

var _ = Describe("EnsureDistContainer", func() {
	var (
		ctx                context.Context
		scheme             *runtime.Scheme
		policy             *weka.WekaPolicy
		namespace          string
		image              string
		serviceAccountName string
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespace = "default"
		image = "quay.io/weka.io/weka-in-container:4.5.0.100"
		serviceAccountName = "weka-runtime"

		scheme = runtime.NewScheme()
		Expect(weka.AddToScheme(scheme)).To(Succeed())

		policy = &weka.WekaPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-policy",
				Namespace: namespace,
				UID:       "test-policy-uid",
			},
		}
	})

	It("should propagate the service account to the dist container", func() {
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		op := &EnsureDistServiceOperation{
			client:  fakeClient,
			scheme:  scheme,
			policy:  policy,
			payload: &weka.DriverDistPayload{},
			containerDetails: weka.WekaOwnerDetails{
				Image:              image,
				ServiceAccountName: serviceAccountName,
			},
		}

		err := op.EnsureDistContainer(ctx)
		Expect(err).NotTo(HaveOccurred())

		var containers weka.WekaContainerList
		err = fakeClient.List(ctx, &containers, &client.ListOptions{Namespace: namespace})
		Expect(err).NotTo(HaveOccurred())
		Expect(containers.Items).To(HaveLen(1))
		Expect(containers.Items[0].Spec.Mode).To(Equal(weka.WekaContainerModeDriversDist))
		Expect(containers.Items[0].Spec.ServiceAccountName).To(Equal(serviceAccountName))
	})
})

var _ = Describe("DeleteIfNodeIneligible", func() {
	const (
		nodeName         = "node-1"
		wcName           = "dist-wc"
		wcNamespace      = "default"
		untoleratedTaint = "medik8s.io/drain" // NoSchedule taint not in the dist container's tolerations
	)

	const operatorNamespace = "weka-operator-system"

	var (
		ctx    context.Context
		scheme *runtime.Scheme
	)

	BeforeEach(func() {
		ctx = context.Background()
		scheme = runtime.NewScheme()
		Expect(weka.AddToScheme(scheme)).To(Succeed())
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
	})

	// A cordon sets Unschedulable and the matching taint; eligibility is decided by the taint.
	cordonedSpec := corev1.NodeSpec{
		Unschedulable: true,
		Taints:        []corev1.Taint{resources.CordonTaint()},
	}

	nodeCondition := func(ready bool) corev1.NodeCondition {
		status := corev1.ConditionTrue
		if !ready {
			status = corev1.ConditionFalse
		}
		return corev1.NodeCondition{Type: corev1.NodeReady, Status: status}
	}

	boundPod := func(phase corev1.PodPhase) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: wcName, Namespace: wcNamespace},
			Spec:       corev1.PodSpec{NodeName: nodeName},
			Status:     corev1.PodStatus{Phase: phase},
		}
	}

	// setTolerateCordon resolves the operator-wide settings from a policy carrying the given value, so
	// the cordon cases can be driven from both sides of the opt-in.
	setTolerateCordon := func(ctx context.Context, value bool) {
		origNamespace := config.Config.OperatorPodNamespace
		config.Config.OperatorPodNamespace = operatorNamespace
		DeferCleanup(func() {
			// Resolve back to the built-in defaults before restoring the namespace: a policy-free client
			// gives the defaults, but only while the namespace this resolved under is still in effect.
			Expect(services.RefreshSettings(ctx, fake.NewClientBuilder().WithScheme(scheme).Build())).To(Succeed())
			config.Config.OperatorPodNamespace = origNamespace
		})

		policy := &weka.WekaPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "configuration", Namespace: operatorNamespace},
			Spec: weka.WekaPolicySpec{
				Payload: weka.PolicyPayload{
					Configuration: &weka.ConfigurationPayload{
						Scheduling: &weka.SchedulingSpec{TolerateCordon: &value},
					},
				},
			},
		}
		policyClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).Build()
		Expect(services.RefreshSettings(ctx, policyClient)).To(Succeed())
	}

	DescribeTable("deletes the dist container only when the node truly can't host it",
		func(node *corev1.Node, pod *corev1.Pod, tolerateCordon bool, expectDeleted bool) {
			setTolerateCordon(ctx, tolerateCordon)

			wc := &weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{Name: wcName, Namespace: wcNamespace},
				Spec:       weka.WekaContainerSpec{Mode: weka.WekaContainerModeDriversDist},
				Status:     weka.WekaContainerStatus{NodeAffinity: weka.NodeName(nodeName)},
			}

			builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(wc)
			if node != nil {
				builder = builder.WithObjects(node)
			}
			if pod != nil {
				builder = builder.WithObjects(pod)
			}
			fakeClient := builder.Build()

			op := &EnsureDistServiceOperation{client: fakeClient}

			key := &weka.WekaContainer{ObjectMeta: metav1.ObjectMeta{Name: wcName, Namespace: wcNamespace}}
			err := op.DeleteIfNodeIneligible(ctx, key)

			var got weka.WekaContainer
			getErr := fakeClient.Get(ctx, client.ObjectKey{Namespace: wcNamespace, Name: wcName}, &got)

			if expectDeleted {
				Expect(err).To(HaveOccurred())
				Expect(apierrors.IsNotFound(getErr)).To(BeTrue())
			} else {
				Expect(err).NotTo(HaveOccurred())
				Expect(getErr).NotTo(HaveOccurred())
			}
		},
		Entry("ready, not cordoned, pod running bound -> kept",
			&corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{nodeCondition(true)}},
			},
			boundPod(corev1.PodRunning),
			false,
			false,
		),
		Entry("not ready + cordoned, pod running bound -> deleted",
			&corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec:       cordonedSpec,
				Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{nodeCondition(false)}},
			},
			boundPod(corev1.PodRunning),
			false,
			true,
		),
		Entry("ready, cordoned, pod running bound -> kept",
			&corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec:       cordonedSpec,
				Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{nodeCondition(true)}},
			},
			boundPod(corev1.PodRunning),
			false,
			false,
		),
		Entry("ready, cordoned, pod bound but pending -> kept",
			&corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec:       cordonedSpec,
				Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{nodeCondition(true)}},
			},
			boundPod(corev1.PodPending),
			false,
			false,
		),
		// Opted in, dist tolerates the cordon, so a cordoned-but-Ready node keeps its dist container even
		// with no live pod — deleting it leaves the node's drivers-loader with no endpoint to pull from.
		Entry("ready, cordoned, no pod, opted in -> kept",
			&corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec:       cordonedSpec,
				Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{nodeCondition(true)}},
			},
			nil,
			true,
			false,
		),
		Entry("ready, cordoned, no pod, not opted in -> deleted",
			&corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec:       cordonedSpec,
				Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{nodeCondition(true)}},
			},
			nil,
			false,
			true,
		),
		Entry("ready, cordoned, pod failed bound, opted in -> kept",
			&corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec:       cordonedSpec,
				Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{nodeCondition(true)}},
			},
			boundPod(corev1.PodFailed),
			true,
			false,
		),
		Entry("ready, cordoned, pod failed bound, not opted in -> deleted",
			&corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec:       cordonedSpec,
				Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{nodeCondition(true)}},
			},
			boundPod(corev1.PodFailed),
			false,
			true,
		),
		Entry("ready, untolerated NoSchedule taint, no pod -> deleted",
			&corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Spec: corev1.NodeSpec{Taints: []corev1.Taint{
					{Key: untoleratedTaint, Effect: corev1.TaintEffectNoSchedule},
				}},
				Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{nodeCondition(true)}},
			},
			nil,
			false,
			true,
		),
		Entry("node missing, no pod -> deleted",
			nil,
			nil,
			false,
			true,
		),
	)
})

var _ = Describe("EnsureBuilderContainers on an ineligible node", func() {
	const (
		namespace = "default"
		image     = "quay.io/weka.io/weka-in-container:4.5.0.100"
		kernel    = "5.15.0-100-generic"
		arch      = "amd64"
		osImage   = "Ubuntu 22.04.5 LTS"
	)

	var (
		ctx    context.Context
		scheme *runtime.Scheme
		policy *weka.WekaPolicy
		ka     nodeAttributes
	)

	BeforeEach(func() {
		ctx = context.Background()
		scheme = runtime.NewScheme()
		Expect(weka.AddToScheme(scheme)).To(Succeed())
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		policy = &weka.WekaPolicy{ObjectMeta: metav1.ObjectMeta{Name: "test-policy", Namespace: namespace, UID: "test-policy-uid"}}
		ka = nodeAttributes{kernelVersion: kernel, architecture: arch, osImage: osImage}
		origNamespace := config.Config.OperatorPodNamespace
		config.Config.OperatorPodNamespace = "weka-operator-system"
		DeferCleanup(func() { config.Config.OperatorPodNamespace = origNamespace })
		Expect(services.RefreshSettings(ctx, fake.NewClientBuilder().WithScheme(scheme).Build())).To(Succeed())
	})

	newOp := func(c client.Client, recorder events.EventRecorder) *EnsureDistServiceOperation {
		return &EnsureDistServiceOperation{
			client:            c,
			scheme:            scheme,
			policy:            policy,
			payload:           &weka.DriverDistPayload{},
			containerDetails:  weka.WekaOwnerDetails{Image: image},
			recorder:          recorder,
			throttler:         throttling.NewSyncMapThrottler(),
			discoveredImages:  map[string]bool{image: true},
			targetKernelArchs: map[string]nodeAttributes{"key1": ka},
			builderNames:      map[string]string{},
		}
	}

	// The builder is pinned to a NotReady node and holds a finalizer, as a live one does while its
	// pod stays Terminating on that node.
	setup := func(status weka.ContainerStatus) (client.Client, string) {
		op := newOp(nil, nil)
		baseName := op.getNewBuilderContainerName(image, kernel, arch, osImage)
		builder := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:       baseName,
				Namespace:  namespace,
				Labels:     op.getBuilderContainerLabels(image, kernel, arch, osImage),
				Finalizers: []string{"weka.weka.io/finalizer"},
			},
			Spec:   weka.WekaContainerSpec{Mode: weka.WekaContainerModeDriversBuilder, Image: image},
			Status: weka.WekaContainerStatus{Status: status, NodeAffinity: "node-x"},
		}
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-x"},
			Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}},
		}
		return fake.NewClientBuilder().WithScheme(scheme).WithObjects(builder, node).Build(), baseName
	}

	listBuilders := func(c client.Client) []weka.WekaContainer {
		var list weka.WekaContainerList
		Expect(c.List(ctx, &list, client.InNamespace(namespace),
			client.MatchingLabels{"weka.io/mode": weka.WekaContainerModeDriversBuilder})).To(Succeed())
		return list.Items
	}

	It("deletes a non-Completed builder and replaces it under a new name", func() {
		c, baseName := setup(weka.Building)
		recorder := events.NewFakeRecorder(10)

		Expect(newOp(c, recorder).EnsureBuilderContainers(ctx)).To(Succeed())
		builders := listBuilders(c)
		Expect(builders).To(HaveLen(1))
		Expect(builders[0].DeletionTimestamp).NotTo(BeNil())
		Expect(<-recorder.Events).To(ContainSubstring("Warning DriversBuilderReplaced"))

		Expect(newOp(c, recorder).EnsureBuilderContainers(ctx)).To(Succeed())
		builders = listBuilders(c)
		Expect(builders).To(HaveLen(2))
		var replacement string
		for _, b := range builders {
			if b.Name != baseName {
				replacement = b.Name
			}
		}
		Expect(replacement).To(HavePrefix(baseName[:min(len(baseName), 58)]))
		Expect(<-recorder.Events).To(ContainSubstring("Warning NoEligibleNode"))

		// a later pass keeps the replacement instead of generating another
		Expect(newOp(c, recorder).EnsureBuilderContainers(ctx)).To(Succeed())
		Expect(listBuilders(c)).To(HaveLen(2))
		name, err := newOp(c, recorder).getBuilderContainerName(ctx, image, ka)
		Expect(err).NotTo(HaveOccurred())
		Expect(name).To(Equal(replacement))
	})

	It("leaves a Completed builder on its node", func() {
		c, baseName := setup(weka.Completed)

		Expect(newOp(c, events.NewFakeRecorder(10)).EnsureBuilderContainers(ctx)).To(Succeed())
		builders := listBuilders(c)
		Expect(builders).To(HaveLen(1))
		Expect(builders[0].Name).To(Equal(baseName))
		Expect(builders[0].DeletionTimestamp).To(BeNil())
	})
})
