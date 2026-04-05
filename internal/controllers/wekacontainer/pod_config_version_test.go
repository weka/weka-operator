package wekacontainer

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/config"
)

var _ = Describe("Pod config version drift detection", func() {
	const (
		testImage  = "quay.io/weka.io/weka-in-container:4.5.0"
		testImage2 = "quay.io/weka.io/weka-in-container:4.6.0"
		clusterUID = "cluster-uid-123"
	)

	var (
		ctx context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
		config.Config.PodConfigVersion = "1"
	})

	Describe("targetPodConfigHash", func() {
		It("returns self-calculated version for ownerless containers", func() {
			container := &weka.WekaContainer{
				Spec: weka.WekaContainerSpec{Image: testImage},
			}
			ver := targetPodConfigHash(container)
			Expect(ver).NotTo(BeEmpty())
			Expect(ver).To(Equal(selfCalcSpecVersion(testImage)))
		})

		It("returns empty string for owned containers without explicit PodConfigHash", func() {
			container := &weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{UID: clusterUID, Name: "my-cluster", Kind: "WekaCluster", APIVersion: "weka.weka.io/v1alpha1"},
					},
				},
				Spec: weka.WekaContainerSpec{Image: testImage},
			}
			Expect(targetPodConfigHash(container)).To(BeEmpty())
		})

		It("returns explicit PodConfigHash for owned containers when set", func() {
			container := &weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{UID: clusterUID, Name: "my-cluster", Kind: "WekaCluster", APIVersion: "weka.weka.io/v1alpha1"},
					},
				},
				Spec: weka.WekaContainerSpec{
					Image:         testImage,
					PodConfigHash: "abc12345",
				},
			}
			Expect(targetPodConfigHash(container)).To(Equal("abc12345"))
		})

		It("changes when image changes", func() {
			v1 := selfCalcSpecVersion(testImage)
			v2 := selfCalcSpecVersion(testImage2)
			Expect(v1).NotTo(Equal(v2))
		})

		It("changes when PodConfigVersion changes", func() {
			config.Config.PodConfigVersion = "1"
			v1 := selfCalcSpecVersion(testImage)
			config.Config.PodConfigVersion = "2"
			v2 := selfCalcSpecVersion(testImage)
			Expect(v1).NotTo(Equal(v2))
		})

		It("changes when EnablePodConfigCodeVersionRotation is toggled", func() {
			config.Config.PodConfigVersion = "1"

			config.Config.EnablePodConfigCodeVersionRotation = false
			hashWithout := selfCalcSpecVersion(testImage)

			config.Config.EnablePodConfigCodeVersionRotation = true
			hashWith := selfCalcSpecVersion(testImage)

			Expect(hashWithout).NotTo(Equal(hashWith))
		})

		It("is stable when EnablePodConfigCodeVersionRotation is false", func() {
			config.Config.PodConfigVersion = "1"
			config.Config.EnablePodConfigCodeVersionRotation = false

			h1 := selfCalcSpecVersion(testImage)
			h2 := selfCalcSpecVersion(testImage)
			Expect(h1).To(Equal(h2))
		})
	})

	Describe("handleSpecVersionMismatch", func() {
		var (
			scheme *runtime.Scheme
		)

		BeforeEach(func() {
			scheme = runtime.NewScheme()
			Expect(v1.AddToScheme(scheme)).To(Succeed())
			Expect(weka.AddToScheme(scheme)).To(Succeed())
		})

		newReconciler := func(container *weka.WekaContainer, pod *v1.Pod) *containerReconcilerLoop {
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(container, pod).
				WithStatusSubresource(container).
				Build()

			return &containerReconcilerLoop{
				Client:    fakeClient,
				container: container,
				pod:       pod,
			}
		}

		Context("ownerless container", func() {
			It("deletes pod when LastAppliedPodConfigHash mismatches target", func() {
				container := &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{Name: "test-c", Namespace: "default", UID: "c-uid"},
					Spec:       weka.WekaContainerSpec{Image: testImage},
					Status: weka.WekaContainerStatus{
						LastAppliedImage:         testImage,
						LastAppliedPodConfigHash: "stale-version",
					},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "test-c", Namespace: "default"},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("pod-config-version mismatch"))
			})

			It("does nothing when LastAppliedPodConfigHash matches target", func() {
				specVer := selfCalcSpecVersion(testImage)
				container := &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{Name: "test-c", Namespace: "default", UID: "c-uid"},
					Spec:       weka.WekaContainerSpec{Image: testImage},
					Status: weka.WekaContainerStatus{
						LastAppliedImage:         testImage,
						LastAppliedPodConfigHash: specVer,
					},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "test-c", Namespace: "default"},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				Expect(err).NotTo(HaveOccurred())
			})

			It("does NOT delete pod when LastAppliedPodConfigHash is empty and allowRotateEmptyPodConfigHash=false", func() {
				config.Config.AllowRotateNonAnnotatedPodConfigHash = false
				container := &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{Name: "test-c", Namespace: "default", UID: "c-uid"},
					Spec:       weka.WekaContainerSpec{Image: testImage},
					Status:     weka.WekaContainerStatus{LastAppliedImage: testImage},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "test-c", Namespace: "default"},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				Expect(err).NotTo(HaveOccurred())
			})

			It("deletes pod when LastAppliedPodConfigHash is empty and allowRotateEmptyPodConfigHash=true", func() {
				config.Config.AllowRotateNonAnnotatedPodConfigHash = true
				container := &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{Name: "test-c", Namespace: "default", UID: "c-uid"},
					Spec:       weka.WekaContainerSpec{Image: testImage},
					Status:     weka.WekaContainerStatus{LastAppliedImage: testImage},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "test-c", Namespace: "default"},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("pod-config-version mismatch"))
			})
		})

		Context("owned container with explicit PodConfigHash", func() {
			It("deletes pod when LastAppliedPodConfigHash mismatches PodConfigHash", func() {
				container := &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-c", Namespace: "default", UID: "c-uid",
						OwnerReferences: []metav1.OwnerReference{
							{UID: clusterUID, Name: "my-cluster", Kind: "WekaCluster", APIVersion: "weka.weka.io/v1alpha1"},
						},
					},
					Spec: weka.WekaContainerSpec{
						Image:         testImage,
						PodConfigHash: "newversion",
					},
					Status: weka.WekaContainerStatus{
						LastAppliedImage:         testImage,
						LastAppliedPodConfigHash: "oldversion",
					},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "test-c", Namespace: "default"},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("pod-config-version mismatch"))
			})

			It("does nothing when LastAppliedPodConfigHash matches PodConfigHash", func() {
				container := &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-c", Namespace: "default", UID: "c-uid",
						OwnerReferences: []metav1.OwnerReference{
							{UID: clusterUID, Name: "my-cluster", Kind: "WekaCluster", APIVersion: "weka.weka.io/v1alpha1"},
						},
					},
					Spec: weka.WekaContainerSpec{
						Image:         testImage,
						PodConfigHash: "myversion",
					},
					Status: weka.WekaContainerStatus{
						LastAppliedImage:         testImage,
						LastAppliedPodConfigHash: "myversion",
					},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "test-c", Namespace: "default"},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("owned container without explicit PodConfigHash", func() {
			It("skips pod config version check entirely", func() {
				container := &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-c", Namespace: "default", UID: "c-uid",
						OwnerReferences: []metav1.OwnerReference{
							{UID: clusterUID, Name: "my-cluster", Kind: "WekaCluster", APIVersion: "weka.weka.io/v1alpha1"},
						},
					},
					Spec:   weka.WekaContainerSpec{Image: testImage},
					Status: weka.WekaContainerStatus{LastAppliedImage: testImage},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "test-c", Namespace: "default"},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("image change gating", func() {
			It("skips handleImageUpdate when image has not changed (spec-version-only change)", func() {
				container := &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-c", Namespace: "default", UID: "c-uid",
						OwnerReferences: []metav1.OwnerReference{
							{UID: clusterUID, Name: "my-cluster", Kind: "WekaCluster", APIVersion: "weka.weka.io/v1alpha1"},
						},
					},
					Spec: weka.WekaContainerSpec{
						Image:         testImage,
						PodConfigHash: "newspecver",
					},
					Status: weka.WekaContainerStatus{
						LastAppliedImage:         testImage, // image matches — no image upgrade
						LastAppliedPodConfigHash: "oldspecver",
					},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "test-c", Namespace: "default"},
					// No weka-container in pod spec — if handleImageUpdate runs, it would error.
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("pod-config-version mismatch"))
			})

			It("runs handleImageUpdate when both image and spec version changed", func() {
				container := &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-c", Namespace: "default", UID: "c-uid",
						OwnerReferences: []metav1.OwnerReference{
							{UID: clusterUID, Name: "my-cluster", Kind: "WekaCluster", APIVersion: "weka.weka.io/v1alpha1"},
						},
					},
					Spec: weka.WekaContainerSpec{
						Image:         testImage2,
						PodConfigHash: "newspecver",
					},
					Status: weka.WekaContainerStatus{
						LastAppliedImage:         testImage, // image differs — triggers handleImageUpdate
						LastAppliedPodConfigHash: "oldspecver",
					},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "test-c", Namespace: "default"},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{Name: "weka-container", Image: testImage},
						},
					},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				// handleImageUpdate runs and returns an error (upgrade conditions, manual policy, etc.)
				// The key point: it does NOT skip to pod-config-version check
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).NotTo(ContainSubstring("pod-config-version mismatch"))
			})

			It("rotates pod via handleImageUpdate when image+PodConfigHash changed, even with empty LastAppliedPodConfigHash", func() {
				container := &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-c", Namespace: "default", UID: "c-uid",
						OwnerReferences: []metav1.OwnerReference{
							{UID: clusterUID, Name: "my-cluster", Kind: "WekaCluster", APIVersion: "weka.weka.io/v1alpha1"},
						},
					},
					Spec: weka.WekaContainerSpec{
						Image:         testImage2,
						PodConfigHash: "newspecver",
					},
					Status: weka.WekaContainerStatus{
						LastAppliedImage: testImage, // image differs
					},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "test-c", Namespace: "default"},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{Name: "weka-container", Image: testImage},
						},
					},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				// handleImageUpdate runs because image changed
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).NotTo(ContainSubstring("pod-config-version mismatch"))

				// Verify pod was deleted by handleImageUpdate
				deletedPod := &v1.Pod{}
				getErr := r.Get(ctx, client.ObjectKeyFromObject(pod), deletedPod)
				Expect(getErr).To(HaveOccurred())
				Expect(apierrors.IsNotFound(getErr)).To(BeTrue())
			})
		})
	})
})
