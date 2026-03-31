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
	"github.com/weka/weka-operator/internal/consts"
)

var _ = Describe("Spec version drift detection", func() {
	const (
		testImage    = "quay.io/weka.io/weka-in-container:4.5.0"
		testImage2   = "quay.io/weka.io/weka-in-container:4.6.0"
		clusterUID   = "cluster-uid-123"
	)

	var (
		ctx context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
		config.Config.PodConfigVersion = "1"
	})

	Describe("targetSpecVersion", func() {
		It("returns self-calculated version for ownerless containers", func() {
			container := &weka.WekaContainer{
				Spec: weka.WekaContainerSpec{Image: testImage},
			}
			ver := targetSpecVersion(container)
			Expect(ver).NotTo(BeEmpty())
			Expect(ver).To(Equal(selfCalcSpecVersion(testImage)))
		})

		It("returns empty string for owned containers without explicit SpecVersion", func() {
			container := &weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{UID: clusterUID, Name: "my-cluster", Kind: "WekaCluster", APIVersion: "weka.weka.io/v1alpha1"},
					},
				},
				Spec: weka.WekaContainerSpec{Image: testImage},
			}
			Expect(targetSpecVersion(container)).To(BeEmpty())
		})

		It("returns explicit SpecVersion for owned containers when set", func() {
			container := &weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{UID: clusterUID, Name: "my-cluster", Kind: "WekaCluster", APIVersion: "weka.weka.io/v1alpha1"},
					},
				},
				Spec: weka.WekaContainerSpec{
					Image:       testImage,
					SpecVersion: "abc12345",
				},
			}
			Expect(targetSpecVersion(container)).To(Equal("abc12345"))
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
			It("deletes pod when spec version annotation mismatches", func() {
				specVer := selfCalcSpecVersion(testImage)
				container := &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{Name: "test-c", Namespace: "default", UID: "c-uid"},
					Spec:       weka.WekaContainerSpec{Image: testImage},
					Status:     weka.WekaContainerStatus{LastAppliedImage: testImage},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-c", Namespace: "default",
						Annotations: map[string]string{
							consts.PodSpecVersionAnnotation: "stale-version",
						},
					},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)

				// Should return a WaitError (pod deleted)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("spec-version mismatch"))
				_ = specVer
			})

			It("does nothing when spec version matches", func() {
				specVer := selfCalcSpecVersion(testImage)
				container := &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{Name: "test-c", Namespace: "default", UID: "c-uid"},
					Spec:       weka.WekaContainerSpec{Image: testImage},
					Status:     weka.WekaContainerStatus{LastAppliedImage: testImage},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-c", Namespace: "default",
						Annotations: map[string]string{
							consts.PodSpecVersionAnnotation: specVer,
						},
					},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("owned container with explicit SpecVersion", func() {
			It("deletes pod when annotation mismatches SpecVersion", func() {
				container := &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-c", Namespace: "default", UID: "c-uid",
						OwnerReferences: []metav1.OwnerReference{
							{UID: clusterUID, Name: "my-cluster", Kind: "WekaCluster", APIVersion: "weka.weka.io/v1alpha1"},
						},
					},
					Spec: weka.WekaContainerSpec{
						Image:       testImage,
						SpecVersion: "newversion",
					},
					Status: weka.WekaContainerStatus{LastAppliedImage: testImage},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-c", Namespace: "default",
						Annotations: map[string]string{
							consts.PodSpecVersionAnnotation: "oldversion",
						},
					},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("spec-version mismatch"))
			})

			It("does nothing when annotation matches SpecVersion", func() {
				container := &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-c", Namespace: "default", UID: "c-uid",
						OwnerReferences: []metav1.OwnerReference{
							{UID: clusterUID, Name: "my-cluster", Kind: "WekaCluster", APIVersion: "weka.weka.io/v1alpha1"},
						},
					},
					Spec: weka.WekaContainerSpec{
						Image:       testImage,
						SpecVersion: "myversion",
					},
					Status: weka.WekaContainerStatus{
						LastAppliedImage:       testImage,
						LastAppliedSpec: "myversion",
					},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-c", Namespace: "default",
						Annotations: map[string]string{
							consts.PodSpecVersionAnnotation: "myversion",
						},
					},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("owned container without explicit SpecVersion", func() {
			It("skips spec version check entirely", func() {
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
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-c", Namespace: "default",
						Annotations: map[string]string{
							consts.PodSpecVersionAnnotation: "whatever-stale",
						},
					},
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
						Image:       testImage,
						SpecVersion: "newspecver",
					},
					Status: weka.WekaContainerStatus{
						LastAppliedImage: testImage, // image matches — no image upgrade
					},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-c", Namespace: "default",
						Annotations: map[string]string{
							consts.PodSpecVersionAnnotation: "oldspecver",
						},
					},
					// No weka-container in pod spec — if handleImageUpdate runs, it would error.
					// The fact that the test succeeds with a WaitError (not a hard error) proves it was skipped.
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("spec-version mismatch"))
			})

			It("rotates pod via handleImageUpdate when image+specVersion changed, even without annotation and allowRotateNonAnnotated=false", func() {
				config.Config.AllowRotateNonAnnotated = false
				container := &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-c", Namespace: "default", UID: "c-uid",
						OwnerReferences: []metav1.OwnerReference{
							{UID: clusterUID, Name: "my-cluster", Kind: "WekaCluster", APIVersion: "weka.weka.io/v1alpha1"},
						},
					},
					Spec: weka.WekaContainerSpec{
						Image:       testImage2,
						SpecVersion: "newspecver",
					},
					Status: weka.WekaContainerStatus{
						LastAppliedImage: testImage, // image differs
					},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test-c",
						Namespace:   "default",
						Annotations: map[string]string{}, // no spec-version annotation
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{Name: "weka-container", Image: testImage},
						},
					},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				// handleImageUpdate runs because image changed — deletes the pod regardless of allowRotateNonAnnotated
				// The function returns nil (pod deleted successfully by image upgrade path)
				Expect(err).NotTo(HaveOccurred())

				// Verify pod was actually deleted
				deletedPod := &v1.Pod{}
				getErr := r.Get(ctx, client.ObjectKeyFromObject(pod), deletedPod)
				Expect(getErr).To(HaveOccurred())
				Expect(apierrors.IsNotFound(getErr)).To(BeTrue())
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
						Image:       testImage2,
						SpecVersion: "newspecver",
					},
					Status: weka.WekaContainerStatus{
						LastAppliedImage: testImage, // image differs — triggers handleImageUpdate
					},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-c", Namespace: "default",
						Annotations: map[string]string{
							consts.PodSpecVersionAnnotation: "oldspecver",
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{Name: "weka-container", Image: testImage},
						},
					},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				// handleImageUpdate runs and returns an error (upgrade conditions, manual policy, etc.)
				// The key point: it does NOT skip to spec-version check
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).NotTo(ContainSubstring("spec-version mismatch"))
			})
		})

		Context("allowRotateNonAnnotated", func() {
			It("does NOT delete pod without annotation when allowRotateNonAnnotated=false", func() {
				config.Config.AllowRotateNonAnnotated = false
				container := &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{Name: "test-c", Namespace: "default", UID: "c-uid"},
					Spec:       weka.WekaContainerSpec{Image: testImage},
					Status:     weka.WekaContainerStatus{LastAppliedImage: testImage},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test-c",
						Namespace:   "default",
						Annotations: map[string]string{}, // no spec-version annotation
					},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				Expect(err).NotTo(HaveOccurred())
			})

			It("deletes pod without annotation when allowRotateNonAnnotated=true", func() {
				config.Config.AllowRotateNonAnnotated = true
				container := &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{Name: "test-c", Namespace: "default", UID: "c-uid"},
					Spec:       weka.WekaContainerSpec{Image: testImage},
					Status:     weka.WekaContainerStatus{LastAppliedImage: testImage},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test-c",
						Namespace:   "default",
						Annotations: map[string]string{}, // no spec-version annotation
					},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("spec-version mismatch"))
			})

			It("does NOT delete owned container pod without annotation when allowRotateNonAnnotated=false, even with explicit SpecVersion", func() {
				config.Config.AllowRotateNonAnnotated = false
				container := &weka.WekaContainer{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-c", Namespace: "default", UID: "c-uid",
						OwnerReferences: []metav1.OwnerReference{
							{UID: clusterUID, Name: "my-cluster", Kind: "WekaCluster", APIVersion: "weka.weka.io/v1alpha1"},
						},
					},
					Spec: weka.WekaContainerSpec{
						Image:       testImage,
						SpecVersion: "cc315cdb",
					},
					Status: weka.WekaContainerStatus{LastAppliedImage: testImage},
				}
				pod := &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test-c",
						Namespace:   "default",
						Annotations: map[string]string{}, // pre-existing pod, no annotation
					},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				Expect(err).NotTo(HaveOccurred())
			})

			It("does NOT delete owned container pod without annotation even when allowRotateNonAnnotated=true if no SpecVersion set", func() {
				config.Config.AllowRotateNonAnnotated = true
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
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test-c",
						Namespace:   "default",
						Annotations: map[string]string{},
					},
				}

				r := newReconciler(container, pod)
				err := r.handleSpecVersionMismatch(ctx)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})
})
