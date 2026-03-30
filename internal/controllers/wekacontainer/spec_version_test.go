package wekacontainer

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
						LastAppliedSpecVersion: "myversion",
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

			It("deletes owned container pod without annotation when explicit SpecVersion is set, even with allowRotateNonAnnotated=false", func() {
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
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("spec-version mismatch"))
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
