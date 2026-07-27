package operations

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

		// Compute expected new-format names
		// Format: {policyName}-builder-{hashFNV(image+arch)}-{normalizedOs}-{kernelNorm}
		kernelNorm := "5-15-0-100-generic"
		normalizedOs := "ubuntu-22-04" // NormalizeOSImageName("Ubuntu 22.04.5 LTS")
		expectedName1 := "test-policy-builder-" + hashFNV(image1+arch) + "-" + normalizedOs + "-" + kernelNorm
		expectedName2 := "test-policy-builder-" + hashFNV(image2+arch) + "-" + normalizedOs + "-" + kernelNorm

		// Verify both builders were created
		var allContainers weka.WekaContainerList
		err = fakeClient.List(ctx, &allContainers, &client.ListOptions{Namespace: namespace})
		Expect(err).NotTo(HaveOccurred())

		builderNames := []string{}
		for _, wc := range allContainers.Items {
			if wc.Spec.Mode == weka.WekaContainerModeDriversBuilder {
				builderNames = append(builderNames, wc.Name)
			}
		}

		Expect(builderNames).To(HaveLen(2), "Exactly two builder containers should be created")
		Expect(builderNames).To(ContainElement(expectedName1))
		Expect(builderNames).To(ContainElement(expectedName2))

		// Verify each builder has the correct image and service account in its spec
		for _, wc := range allContainers.Items {
			if wc.Spec.Mode == weka.WekaContainerModeDriversBuilder {
				Expect(wc.Spec.ServiceAccountName).To(Equal(serviceAccountName))
			}
			if wc.Name == expectedName1 {
				Expect(wc.Spec.Image).To(Equal(image1))
			}
			if wc.Name == expectedName2 {
				Expect(wc.Spec.Image).To(Equal(image2))
			}
		}
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
