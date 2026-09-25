package wekacontainer

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
)

var _ = DescribeTable("shouldReapExitedPod",
	func(mode string, phase corev1.PodPhase, want bool) {
		r := &containerReconcilerLoop{
			container: &weka.WekaContainer{Spec: weka.WekaContainerSpec{Mode: mode}},
			pod:       &corev1.Pod{Status: corev1.PodStatus{Phase: phase}},
		}
		Expect(r.shouldReapExitedPod()).To(Equal(want))
	},
	Entry("failed client pod (UnexpectedAdmissionError after undrained reboot)", weka.WekaContainerModeClient, corev1.PodFailed, true),
	Entry("succeeded client pod", weka.WekaContainerModeClient, corev1.PodSucceeded, true),
	Entry("running client pod", weka.WekaContainerModeClient, corev1.PodRunning, false),
	Entry("failed backend pod", weka.WekaContainerModeDrive, corev1.PodFailed, true),
	Entry("pending backend pod", weka.WekaContainerModeCompute, corev1.PodPending, false),
	Entry("failed drivers-loader pod", weka.WekaContainerModeDriversLoader, corev1.PodFailed, false),
)
