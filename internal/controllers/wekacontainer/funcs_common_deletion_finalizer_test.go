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
