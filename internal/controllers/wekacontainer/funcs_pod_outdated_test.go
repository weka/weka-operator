package wekacontainer

import (
	"context"
	"encoding/json"
	"strings"
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

	newPod := func(hash string) *v1.Pod {
		p := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
			Status:     v1.PodStatus{Phase: v1.PodRunning},
		}
		if hash != "" {
			p.Annotations = map[string]string{consts.PodConfigHashAnnotation: hash}
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

	diff := []string{"CORES 2→4"}

	activeStep := func(r *containerReconcilerLoop, name string) lifecycle.Step {
		for _, step := range ActiveStateFlow(r) {
			if strings.HasPrefix(step.GetName(), name) {
				return step
			}
		}
		Fail("no active-state step " + name)
		return nil
	}

	Describe("podConfigHash", func() {
		It("is stable", func() {
			Expect(podConfigHash()).To(Equal(podConfigHash()))
			Expect(podConfigHash()).NotTo(BeEmpty())
		})

		It("changes with POD_CONFIG_VERSION", func() {
			config.Config.PodConfigVersion = "1"
			before := podConfigHash()
			config.Config.PodConfigVersion = "2"

			Expect(podConfigHash()).NotTo(Equal(before))
		})

		It("changes with ENABLE_POD_CONFIG_CODE_VERSION_ROTATION", func() {
			config.Config.PodConfigVersion = "1"
			without := podConfigHash()
			config.Config.EnablePodConfigCodeVersionRotation = true

			Expect(podConfigHash()).NotTo(Equal(without))
		})
	})

	Describe("checkPodOutdated", func() {
		It("adopts a pod with no hash annotation without flagging it", func() {
			c := newContainer(weka.WekaContainerModeDrive, "WekaCluster", 4)
			r := newReconciler(c, newPod(""))

			Expect(r.checkPodOutdated(ctx)).To(Succeed())

			got := &v1.Pod{}
			Expect(r.Get(ctx, client.ObjectKeyFromObject(r.pod), got)).To(Succeed())
			Expect(got.Annotations[consts.PodConfigHashAnnotation]).To(Equal(podConfigHash()))
			Expect(storedContainer(r).Status.PodOutdated).To(BeFalse())
			Expect(eventCount()).To(BeZero())
		})

		It("flags a pod that differs from the expected pod and emits one event", func() {
			c := newContainer(weka.WekaContainerModeDrive, "WekaCluster", 4)
			r := newReconciler(c, newPod(podConfigHash()))

			Expect(r.markPodOutdated(ctx, diff)).To(Succeed())

			Expect(storedContainer(r).Status.PodOutdated).To(BeTrue())
			Expect(eventCount()).To(Equal(1))
			ev := <-recorder.Events
			Expect(ev).To(ContainSubstring("Warning"))
			Expect(ev).To(ContainSubstring("PodOutdated"))
			Expect(ev).To(ContainSubstring("CORES 2→4"))
		})

		It("emits no second event while already outdated", func() {
			c := newContainer(weka.WekaContainerModeDrive, "WekaCluster", 4)
			r := newReconciler(c, newPod(podConfigHash()))
			Expect(r.markPodOutdated(ctx, diff)).To(Succeed())
			Expect(eventCount()).To(Equal(1))

			Expect(r.checkPodOutdated(ctx)).To(Succeed())

			Expect(eventCount()).To(Equal(1))
		})

		It("runs the check step for a backend and an ssdproxy but not for a client or envoy", func() {
			for _, tc := range []struct {
				mode, owner string
				runs        bool
			}{
				{weka.WekaContainerModeDrive, "WekaCluster", true},
				{weka.WekaContainerModeSSDProxy, "", true},
				{weka.WekaContainerModeClient, "WekaClient", false},
				{weka.WekaContainerModeEnvoy, "WekaClient", false},
			} {
				c := newContainer(tc.mode, tc.owner, 4)
				c.Spec.NodeAffinity = "node-1"
				r := newReconciler(c, newPod(""))

				runs := true
				for _, predicate := range activeStep(r, "checkPodOutdated").GetPredicates() {
					runs = runs && predicate()
				}
				Expect(runs).To(Equal(tc.runs), tc.mode)
			}
		})

		It("flags an ssdproxy", func() {
			c := newContainer(weka.WekaContainerModeSSDProxy, "", 1)
			r := newReconciler(c, newPod(podConfigHash()))

			Expect(r.detectsPodOutdated()).To(BeTrue())
			Expect(r.markPodOutdated(ctx, diff)).To(Succeed())

			Expect(storedContainer(r).Status.PodOutdated).To(BeTrue())
		})

		It("does not flag when the expected pod cannot be built", func() {
			c := newContainer(weka.WekaContainerModeDrive, "WekaCluster", 4)
			r := newReconciler(c, newPod(podConfigHash()))

			Expect(r.checkPodOutdated(ctx)).To(Succeed())

			Expect(storedContainer(r).Status.PodOutdated).To(BeFalse())
			Expect(eventCount()).To(BeZero())
		})

		It("does not flag when there is no diff", func() {
			c := newContainer(weka.WekaContainerModeDrive, "WekaCluster", 4)
			r := newReconciler(c, newPod(podConfigHash()))

			Expect(r.markPodOutdated(ctx, nil)).To(Succeed())

			Expect(storedContainer(r).Status.PodOutdated).To(BeFalse())
			Expect(eventCount()).To(BeZero())
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

		clearThrottler := func(r *containerReconcilerLoop) (throttling.Throttler, *throttling.ThrottlingSettings) {
			settings := &throttling.ThrottlingSettings{Interval: config.Consts.PodOutdatedCheckInterval, EnsureStepSuccess: true}
			throttler := r.ThrottlingMap.WithPartition(containerThrottlePartition(r.container.Name))
			throttler.SetNow(clearPodOutdatedThrottleKey)
			Expect(throttler.ShouldRun(clearPodOutdatedThrottleKey, settings)).To(BeFalse())
			return throttler, settings
		}

		It("deletes the pod and resets the clear step's throttle when the pod differs", func() {
			c := newApproved(weka.WekaContainerModeCompute, "WekaCluster")
			r := newReconciler(c, newPod(podConfigHash()))
			throttler, settings := clearThrottler(r)

			err := r.rotateOnDiff(ctx, diff)

			var waitErr *lifecycle.WaitError
			Expect(err).To(BeAssignableToTypeOf(waitErr))
			Expect(err.Error()).To(ContainSubstring("CORES 2→4"))
			Expect(podGone(r)).To(BeTrue())
			Expect(throttler.ShouldRun(clearPodOutdatedThrottleKey, settings)).To(BeTrue())
		})

		It("does nothing when the pod matches", func() {
			c := newApproved(weka.WekaContainerModeCompute, "WekaCluster")
			r := newReconciler(c, newPod(podConfigHash()))
			throttler, settings := clearThrottler(r)

			Expect(r.rotateOnDiff(ctx, nil)).To(Succeed())

			Expect(podGone(r)).To(BeFalse())
			Expect(throttler.ShouldRun(clearPodOutdatedThrottleKey, settings)).To(BeFalse())
		})

		It("never deletes the pod when the expected pod cannot be built", func() {
			c := newApproved(weka.WekaContainerModeCompute, "WekaCluster")
			r := newReconciler(c, newPod(podConfigHash()))

			Expect(r.rotateOutdatedPod(ctx)).To(Succeed())

			Expect(podGone(r)).To(BeFalse())
		})

		It("ignores a hand-set rotatePod on an ssdproxy", func() {
			c := newApproved(weka.WekaContainerModeSSDProxy, "")
			r := newReconciler(c, newPod(podConfigHash()))

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

		newOutdated := func() *containerReconcilerLoop {
			c := newContainer(weka.WekaContainerModeDrive, "WekaCluster", 4)
			c.Status.PodOutdated = true
			return newReconciler(c, newPod(podConfigHash()))
		}

		It("clears after the gates when the pod matches", func() {
			r := newOutdated()
			r.localContainer = readyLocal("")

			Expect(r.clearOnMatch(ctx, nil)).To(Succeed())

			Expect(storedContainer(r).Status.PodOutdated).To(BeFalse())
		})

		It("keeps podOutdated while IO processes are not up", func() {
			r := newOutdated()
			r.localContainer = readyLocal("15011")

			err := r.clearOnMatch(ctx, nil)

			var waitErr *lifecycle.WaitError
			Expect(err).To(HaveOccurred())
			Expect(err).To(BeAssignableToTypeOf(waitErr))
			Expect(storedContainer(r).Status.PodOutdated).To(BeTrue())
		})

		It("keeps podOutdated while the pod still differs", func() {
			r := newOutdated()
			r.localContainer = readyLocal("")

			Expect(r.clearOnMatch(ctx, diff)).To(Succeed())

			Expect(storedContainer(r).Status.PodOutdated).To(BeTrue())
		})

		It("does not re-stamp the throttle after a rotation while the old pod is terminating", func() {
			r := newOutdated()
			clearStep := activeStep(r, "clearPodOutdated")
			throttler := r.ThrottlingMap.WithPartition(containerThrottlePartition(r.container.Name))
			settings := &throttling.ThrottlingSettings{Interval: config.Consts.PodOutdatedCheckInterval, EnsureStepSuccess: true}
			engine := lifecycle.StepsEngine{Throttler: throttler, Steps: []lifecycle.Step{clearStep}}
			throttler.Reset(clearPodOutdatedThrottleKey)

			now := metav1.Now()
			r.pod.DeletionTimestamp = &now
			Expect(engine.Run(ctx)).To(Succeed())

			Expect(throttler.ShouldRun(clearPodOutdatedThrottleKey, settings)).To(BeTrue())

			r.pod.DeletionTimestamp = nil
			Expect(engine.Run(ctx)).To(Succeed())

			Expect(throttler.ShouldRun(clearPodOutdatedThrottleKey, settings)).To(BeFalse())
		})

		It("lets the rest of the flow run while it waits for a matching pod to be ready", func() {
			Expect(activeStep(newOutdated(), "clearPodOutdated").ShouldContinueOnError()).To(BeTrue())
		})

		It("keeps podOutdated when the expected pod cannot be built", func() {
			r := newOutdated()
			r.localContainer = readyLocal("")

			Expect(r.clearPodOutdated(ctx)).To(Succeed())

			Expect(storedContainer(r).Status.PodOutdated).To(BeTrue())
		})
	})
})
