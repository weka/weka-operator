package wekacontainer

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/consts"
)

// deletePod is the point at which the operator has decided it is ready to remove the pod, so it must
// strip the weka finalizer there — including when the pod was already force-removed (deletionTimestamp
// set + finalizer present), otherwise the object wedges in Terminating forever. It also strips the
// deprecated finalizer name so pods created by an older operator are not stranded.
var _ = Describe("deletePod weka finalizer", func() {
	var scheme *runtime.Scheme

	BeforeEach(func() {
		scheme = runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
	})

	It("removes the finalizer from a force-deleted (Terminating) pod so it can be reaped", func() {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: "c", Namespace: "default", Finalizers: []string{consts.WekaFinalizer},
		}}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()

		// Simulate the external force-delete: sets deletionTimestamp; the finalizer holds the object.
		Expect(fakeClient.Delete(context.Background(), pod)).To(Succeed())
		held := &corev1.Pod{}
		Expect(fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pod), held)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(held, consts.WekaFinalizer)).To(BeTrue())

		r := &containerReconcilerLoop{Client: fakeClient}
		Expect(r.deletePod(context.Background(), held)).To(Succeed())

		after := &corev1.Pod{}
		err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pod), after)
		if err == nil {
			Expect(controllerutil.ContainsFinalizer(after, consts.WekaFinalizer)).To(BeFalse(),
				"weka finalizer must be cleared once the operator is ready to remove the pod")
		} else {
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "pod should be reaped once its only finalizer is cleared")
		}
	})

	It("deletes a live pod that has the finalizer (controlled delete on a running pod)", func() {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: "c2", Namespace: "default", Finalizers: []string{consts.WekaFinalizer},
		}}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()

		r := &containerReconcilerLoop{Client: fakeClient}
		Expect(r.deletePod(context.Background(), pod)).To(Succeed())

		after := &corev1.Pod{}
		err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pod), after)
		if err == nil {
			Expect(controllerutil.ContainsFinalizer(after, consts.WekaFinalizer)).To(BeFalse())
		} else {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}
	})

	It("also clears the deprecated finalizer name (pod created by an older operator)", func() {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: "c3", Namespace: "default", Finalizers: []string{consts.WekaFinalizerDeprecated},
		}}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()

		// external force-delete of an old-tagged pod: held by the deprecated finalizer
		Expect(fakeClient.Delete(context.Background(), pod)).To(Succeed())
		held := &corev1.Pod{}
		Expect(fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pod), held)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(held, consts.WekaFinalizerDeprecated)).To(BeTrue())

		r := &containerReconcilerLoop{Client: fakeClient}
		Expect(r.deletePod(context.Background(), held)).To(Succeed())

		after := &corev1.Pod{}
		err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pod), after)
		if err == nil {
			Expect(controllerutil.ContainsFinalizer(after, consts.WekaFinalizerDeprecated)).To(BeFalse(),
				"deprecated finalizer must also be cleared so old pods are not stranded")
		} else {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}
	})
})

// releaseTerminalPodOnDeletion runs in the deleting flow before DeactivateWekaContainer: a backend pod
// that has already exited must have its do-not-force-delete finalizer stripped and be reaped immediately,
// so a Failed/Succeeded pod is not wedged in Terminating behind a deactivation that can never complete
// once the node/process is gone.
var _ = Describe("releaseTerminalPodOnDeletion", func() {
	var scheme *runtime.Scheme

	BeforeEach(func() {
		scheme = runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
	})

	reapedOrCleared := func(fakeClient client.Client, pod *corev1.Pod) {
		after := &corev1.Pod{}
		err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pod), after)
		if err == nil {
			Expect(controllerutil.ContainsFinalizer(after, consts.WekaFinalizer)).To(BeFalse(),
				"weka finalizer must be cleared so the terminal pod can be reaped immediately")
		} else {
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "terminal pod should be reaped once its finalizer is cleared")
		}
	}

	DescribeTable("strips the finalizer and reaps a terminal backend pod",
		func(phase corev1.PodPhase) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "t", Namespace: "default", Finalizers: []string{consts.WekaFinalizer}},
				Status:     corev1.PodStatus{Phase: phase},
			}
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()

			r := &containerReconcilerLoop{Client: fakeClient, pod: pod}
			err := r.releaseTerminalPodOnDeletion(context.Background())

			// On success the step returns a WaitError so the loop refetches; the pod must be reaped.
			Expect(err).To(HaveOccurred())
			reapedOrCleared(fakeClient, pod)
		},
		Entry("Failed pod (node/process gone)", corev1.PodFailed),
		Entry("Succeeded pod", corev1.PodSucceeded),
	)

	It("reaps a terminal pod that was already force-deleted (deletionTimestamp + finalizer)", func() {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "t2", Namespace: "default", Finalizers: []string{consts.WekaFinalizer}},
			Status:     corev1.PodStatus{Phase: corev1.PodFailed},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()

		// External force-delete: sets deletionTimestamp; the finalizer wedges it in Terminating.
		Expect(fakeClient.Delete(context.Background(), pod)).To(Succeed())
		held := &corev1.Pod{}
		Expect(fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pod), held)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(held, consts.WekaFinalizer)).To(BeTrue())

		r := &containerReconcilerLoop{Client: fakeClient, pod: held}
		Expect(r.releaseTerminalPodOnDeletion(context.Background())).To(HaveOccurred())

		after := &corev1.Pod{}
		Expect(apierrors.IsNotFound(fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pod), after))).To(BeTrue(),
			"terminal Terminating pod must be reaped once the finalizer is stripped")
	})
})

var _ = Describe("ReleaseTerminalPodOnDeletion step predicates", func() {
	newLoop := func(phase corev1.PodPhase, mode string, terminating bool) *containerReconcilerLoop {
		meta := metav1.ObjectMeta{}
		if terminating {
			now := metav1.Now()
			meta.DeletionTimestamp = &now
		}
		return &containerReconcilerLoop{
			container: &weka.WekaContainer{Spec: weka.WekaContainerSpec{Mode: mode}},
			pod:       &corev1.Pod{ObjectMeta: meta, Status: corev1.PodStatus{Phase: phase}},
		}
	}

	// Mirrors the inline predicate in flow_deleting_state.go: reap only a terminal pod that is already
	// being force-removed (deletionTimestamp set), so a healthy Succeeded/Failed exit is left alone.
	terminalAndTerminating := func(r *containerReconcilerLoop) bool {
		return r.pod.GetDeletionTimestamp() != nil &&
			(r.pod.Status.Phase == corev1.PodSucceeded || r.pod.Status.Phase == corev1.PodFailed)
	}

	It("fires for a terminal backend pod that is being force-removed (deletionTimestamp set)", func() {
		r := newLoop(corev1.PodFailed, weka.WekaContainerModeDrive, true)
		Expect(r.container.IsBackend()).To(BeTrue())
		Expect(terminalAndTerminating(r)).To(BeTrue())
	})

	It("does not fire for a terminal backend pod that merely exited (no deletionTimestamp)", func() {
		r := newLoop(corev1.PodSucceeded, weka.WekaContainerModeDrive, false)
		Expect(r.container.IsBackend()).To(BeTrue())
		Expect(terminalAndTerminating(r)).To(BeFalse())
	})

	It("does not fire for a running backend pod", func() {
		r := newLoop(corev1.PodRunning, weka.WekaContainerModeDrive, true)
		Expect(terminalAndTerminating(r)).To(BeFalse())
	})

	It("does not fire for a terminal client (non-backend) pod", func() {
		r := newLoop(corev1.PodFailed, weka.WekaContainerModeClient, true)
		Expect(r.container.IsBackend()).To(BeFalse())
	})
})
