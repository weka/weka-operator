package wekacontainer

import (
	"context"
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/services"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

var _ = Describe("pod outdated", func() {
	var (
		ctx          context.Context
		scheme       *runtime.Scheme
		recorder     *events.FakeRecorder
		origSettle   time.Duration
		origCodeRot  bool
		origPodCfgVr string
	)

	BeforeEach(func() {
		ctx = context.Background()
		scheme = runtime.NewScheme()
		Expect(v1.AddToScheme(scheme)).To(Succeed())
		Expect(weka.AddToScheme(scheme)).To(Succeed())
		recorder = events.NewFakeRecorder(10)
		origSettle = config.Config.Timeouts.WaitSinceIoProcessesUpTimeout
		origCodeRot = config.Config.EnablePodConfigCodeVersionRotation
		origPodCfgVr = config.Config.PodConfigVersion
		config.Config.Timeouts.WaitSinceIoProcessesUpTimeout = 0
		config.Config.EnablePodConfigCodeVersionRotation = false
	})

	AfterEach(func() {
		config.Config.Timeouts.WaitSinceIoProcessesUpTimeout = origSettle
		config.Config.EnablePodConfigCodeVersionRotation = origCodeRot
		config.Config.PodConfigVersion = origPodCfgVr
	})

	newContainer := func(mode string, owner string, cores int) *weka.WekaContainer {
		c := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default", UID: "c-uid"},
			Spec:       weka.WekaContainerSpec{Mode: mode, NumCores: cores, Hugepages: 100},
			Status:     weka.WekaContainerStatus{Status: weka.Running},
		}
		if owner != "" {
			c.OwnerReferences = []metav1.OwnerReference{{Kind: owner, Name: "o", UID: types.UID("o-uid"), APIVersion: "weka.weka.io/v1alpha1"}}
		}
		return c
	}

	stampOf := func(c *weka.WekaContainer, cores int) string {
		c = c.DeepCopy()
		c.Spec.NumCores = cores
		b, err := json.Marshal(snapshotPodSpec(c))
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}

	newPod := func(stamp string) *v1.Pod {
		p := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
			Status:     v1.PodStatus{Phase: v1.PodRunning},
		}
		if stamp != "" {
			p.Annotations = map[string]string{consts.PodSpecAnnotation: stamp}
		}
		return p
	}

	newReconciler := func(c *weka.WekaContainer, p *v1.Pod) *containerReconcilerLoop {
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(c, p).
			WithStatusSubresource(c).
			Build()
		return &containerReconcilerLoop{
			Client:        fakeClient,
			Recorder:      recorder,
			ThrottlingMap: throttling.NewSyncMapThrottler(),
			container:     c,
			pod:           p,
			_cluster:      &weka.WekaCluster{},
		}
	}

	storedContainer := func(r *containerReconcilerLoop) *weka.WekaContainer {
		got := &weka.WekaContainer{}
		Expect(r.Get(ctx, client.ObjectKeyFromObject(r.container), got)).To(Succeed())
		return got
	}

	eventCount := func() int { return len(recorder.Events) }

	Describe("checkPodOutdated", func() {
		It("adopts a pod with no stamp", func() {
			c := newContainer(weka.WekaContainerModeDrive, "WekaCluster", 4)
			r := newReconciler(c, newPod(""))

			Expect(r.checkPodOutdated(ctx)).To(Succeed())

			got := &v1.Pod{}
			Expect(r.Get(ctx, client.ObjectKeyFromObject(r.pod), got)).To(Succeed())
			Expect(got.Annotations[consts.PodSpecAnnotation]).To(Equal(stampOf(c, 4)))
			Expect(storedContainer(r).Status.PodOutdated).To(BeFalse())
			Expect(eventCount()).To(BeZero())
		})

		It("flags a drifted pod and emits one event", func() {
			c := newContainer(weka.WekaContainerModeDrive, "WekaCluster", 4)
			r := newReconciler(c, newPod(stampOf(c, 2)))

			Expect(r.checkPodOutdated(ctx)).To(Succeed())

			Expect(storedContainer(r).Status.PodOutdated).To(BeTrue())
			Expect(eventCount()).To(Equal(1))
			ev := <-recorder.Events
			Expect(ev).To(ContainSubstring("Warning"))
			Expect(ev).To(ContainSubstring("PodOutdated"))
			Expect(ev).To(ContainSubstring("numCores 2→4"))
		})

		It("emits no second event while already outdated", func() {
			c := newContainer(weka.WekaContainerModeDrive, "WekaCluster", 4)
			r := newReconciler(c, newPod(stampOf(c, 2)))
			Expect(r.checkPodOutdated(ctx)).To(Succeed())
			Expect(eventCount()).To(Equal(1))

			r.container.Spec.NumCores = 8
			Expect(r.checkPodOutdated(ctx)).To(Succeed())

			Expect(eventCount()).To(Equal(1))
		})

		It("does not flag a client or envoy", func() {
			for _, mode := range []string{weka.WekaContainerModeClient, weka.WekaContainerModeEnvoy} {
				c := newContainer(mode, "WekaClient", 4)
				r := newReconciler(c, newPod(stampOf(c, 2)))

				Expect(r.detectsPodOutdated()).To(BeFalse())
				Expect(r.checkPodOutdated(ctx)).To(Succeed())
				Expect(storedContainer(r).Status.PodOutdated).To(BeFalse())
			}
		})

		It("flags an ssdproxy", func() {
			c := newContainer(weka.WekaContainerModeSSDProxy, "", 1)
			p := newPod(stampOf(c, 1))
			c.Spec.Hugepages = 200
			r := newReconciler(c, p)

			Expect(r.checkPodOutdated(ctx)).To(Succeed())

			Expect(storedContainer(r).Status.PodOutdated).To(BeTrue())
		})
	})

	Describe("rotateOutdatedPod", func() {
		newApproved := func(mode, owner string) *weka.WekaContainer {
			c := newContainer(mode, owner, 4)
			c.Spec.RotatePod = true
			c.Spec.Image = "img:1"
			c.Status.LastAppliedImage = "img:1"
			c.Status.PodOutdated = true
			return c
		}

		podGone := func(r *containerReconcilerLoop) bool {
			got := &v1.Pod{}
			err := r.Get(ctx, client.ObjectKeyFromObject(r.pod), got)
			return apierrors.IsNotFound(err) || got.DeletionTimestamp != nil
		}

		It("deletes the pod when approved and outdated", func() {
			c := newApproved(weka.WekaContainerModeCompute, "WekaCluster")
			r := newReconciler(c, newPod(stampOf(c, 2)))

			err := r.rotateOutdatedPod(ctx)

			var waitErr *lifecycle.WaitError
			Expect(err).To(BeAssignableToTypeOf(waitErr))
			Expect(podGone(r)).To(BeTrue())
		})

		It("does nothing on a revert", func() {
			c := newApproved(weka.WekaContainerModeCompute, "WekaCluster")
			r := newReconciler(c, newPod(stampOf(c, 4)))

			Expect(r.rotateOutdatedPod(ctx)).To(Succeed())
			Expect(podGone(r)).To(BeFalse())
		})

		It("ignores a hand-set rotatePod on an ssdproxy", func() {
			c := newApproved(weka.WekaContainerModeSSDProxy, "")
			r := newReconciler(c, newPod(stampOf(c, 4)))

			Expect(r.podRotationApproved()).To(BeFalse())
		})
	})

	Describe("clearPodOutdated", func() {
		readyLocal := func(ioNotUp string) *services.WekaLocalContainer {
			lc := &services.WekaLocalContainer{}
			if ioNotUp != "" {
				lc.InternalStatus.IoProcessesNotUpRaw = json.RawMessage(`"` + ioNotUp + `"`)
			}
			return lc
		}

		It("clears after the gates", func() {
			c := newContainer(weka.WekaContainerModeDrive, "WekaCluster", 4)
			c.Status.PodOutdated = true
			r := newReconciler(c, newPod(stampOf(c, 4)))
			r.localContainer = readyLocal("")

			Expect(r.clearPodOutdated(ctx)).To(Succeed())

			Expect(storedContainer(r).Status.PodOutdated).To(BeFalse())
		})

		It("keeps podOutdated while IO processes are not up", func() {
			c := newContainer(weka.WekaContainerModeDrive, "WekaCluster", 4)
			c.Status.PodOutdated = true
			r := newReconciler(c, newPod(stampOf(c, 4)))
			r.localContainer = readyLocal("15011")

			err := r.clearPodOutdated(ctx)

			var waitErr *lifecycle.WaitError
			Expect(err).To(HaveOccurred())
			Expect(err).To(BeAssignableToTypeOf(waitErr))
			Expect(storedContainer(r).Status.PodOutdated).To(BeTrue())
		})

		It("does nothing while stamp differs from spec", func() {
			c := newContainer(weka.WekaContainerModeDrive, "WekaCluster", 4)
			c.Status.PodOutdated = true
			r := newReconciler(c, newPod(stampOf(c, 2)))

			Expect(r.clearPodOutdated(ctx)).To(Succeed())

			Expect(storedContainer(r).Status.PodOutdated).To(BeTrue())
		})
	})
})
