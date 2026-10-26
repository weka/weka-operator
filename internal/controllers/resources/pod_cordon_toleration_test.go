package resources

import (
	"fmt"
	"slices"
	"testing"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services"
)

// TestCordonToleration pins which modes may run on a cordoned node once the configuration policy opts
// in: the client and the node-local pods it depends on, so a node cordoned pending validation can still
// pass a storage health check; never backends. With the setting off no mode tolerates it. Both settings
// of the skip* flags are covered because neither may switch the cordon toleration off — a deployment
// running with skipClientNoScheduleToleration still needs it once opted in.
func TestCordonToleration(t *testing.T) {
	cases := []struct {
		name string
		mode string
		// wantOptedIn is the expectation with scheduling.tolerateCordon enabled; with it disabled no
		// mode tolerates the cordon.
		wantOptedIn bool
	}{
		{name: "client", mode: weka.WekaContainerModeClient, wantOptedIn: true},
		{name: "drivers-loader", mode: weka.WekaContainerModeDriversLoader, wantOptedIn: true},
		{name: "drivers-dist", mode: weka.WekaContainerModeDriversDist, wantOptedIn: true},
		{name: "discovery", mode: weka.WekaContainerModeDiscovery, wantOptedIn: true},
		{name: "adhoc-op", mode: weka.WekaContainerModeAdhocOp, wantOptedIn: true},
		{name: "adhoc-op-with-container", mode: weka.WekaContainerModeAdhocOpWC, wantOptedIn: true},
		{name: "drive", mode: weka.WekaContainerModeDrive, wantOptedIn: false},
		{name: "compute", mode: weka.WekaContainerModeCompute, wantOptedIn: false},
	}

	for _, tolerateCordon := range []bool{false, true} {
		for _, skipNoSchedule := range []bool{false, true} {
			for _, tc := range cases {
				name := fmt.Sprintf("%s/tolerateCordon=%v/skipNoSchedule=%v", tc.name, tolerateCordon, skipNoSchedule)
				t.Run(name, func(t *testing.T) {
					origClient := config.Config.SkipClientNoScheduleToleration
					origAux := config.Config.SkipAuxNoScheduleToleration
					config.Config.SkipClientNoScheduleToleration = skipNoSchedule
					config.Config.SkipAuxNoScheduleToleration = skipNoSchedule
					defer func() {
						config.Config.SkipClientNoScheduleToleration = origClient
						config.Config.SkipAuxNoScheduleToleration = origAux
					}()

					want := tolerateCordon && tc.wantOptedIn
					container := &weka.WekaContainer{Spec: weka.WekaContainerSpec{Mode: tc.mode}}
					scheduling := services.SchedulingSettings{TolerateCordon: tolerateCordon}
					if got := ToleratesCordon(container, scheduling); got != want {
						t.Errorf("ToleratesCordon(%s, %v) = %v, want %v", tc.mode, tolerateCordon, got, want)
					}

					// ToleratesCordon answers from the policy and the mode; GetWekaPodTolerations
					// decides separately whether to put the toleration on the pod. Nothing but this
					// assertion keeps the two conditions in step, and a drift between them creates a
					// pod the operator believes may run on a cordoned node but the scheduler rejects.
					hasToleration := slices.Contains(GetWekaPodTolerations(container, scheduling), GetUnschedulableToleration())
					if hasToleration != want {
						t.Errorf("GetWekaPodTolerations(%s, %v) carries cordon toleration = %v, want %v (must agree with ToleratesCordon)",
							tc.mode, tolerateCordon, hasToleration, want)
					}
				})
			}
		}
	}
}

// TestCordonTolerationIgnoresContainerTolerations pins that the opt-in cannot be granted sideways. A
// cluster-wide catch-all lands on every container's spec, and a keyless Operator=Exists tolerates the
// cordon taint like any other NoSchedule taint — so answering from the pod's own tolerations would put
// backends on cordoned nodes, which cordonTolerantModes exists to prevent. An explicitly keyed cordon
// toleration is covered too: on a backend the mode decides, not the toleration.
func TestCordonTolerationIgnoresContainerTolerations(t *testing.T) {
	tolerations := []struct {
		name string
		tol  corev1.Toleration
	}{
		{name: "keyless-exists", tol: corev1.Toleration{Operator: corev1.TolerationOpExists}},
		{name: "keyless-exists-noschedule", tol: corev1.Toleration{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}},
		{name: "explicit-cordon-toleration", tol: GetUnschedulableToleration()},
	}

	for _, mode := range []string{weka.WekaContainerModeDrive, weka.WekaContainerModeCompute} {
		for _, tt := range tolerations {
			t.Run(fmt.Sprintf("%s/%s", mode, tt.name), func(t *testing.T) {
				container := &weka.WekaContainer{Spec: weka.WekaContainerSpec{
					Mode:        mode,
					Tolerations: []corev1.Toleration{tt.tol},
				}}
				if ToleratesCordon(container, services.SchedulingSettings{TolerateCordon: true}) {
					t.Errorf("backend mode %s must not tolerate the cordon through its own tolerations", mode)
				}
			})
		}
	}
}

// TestUnhealthyTolerationsExcludeCordon guards the reason the cordon toleration lives on its own: the
// unschedulable taint is only ever applied with NoSchedule, so listing it among the NoExecute
// node-health tolerations matched nothing, and SkipUnhealthyToleration must not reach it.
func TestUnhealthyTolerationsExcludeCordon(t *testing.T) {
	for _, tol := range getUnhealthyTolerations() {
		if tol.Key == CordonTaintKey {
			t.Errorf("cordon toleration must not be in getUnhealthyTolerations, found %+v", tol)
		}
	}

	orig := config.Config.SkipUnhealthyToleration
	config.Config.SkipUnhealthyToleration = true
	defer func() { config.Config.SkipUnhealthyToleration = orig }()

	container := &weka.WekaContainer{Spec: weka.WekaContainerSpec{Mode: weka.WekaContainerModeClient}}
	if !ToleratesCordon(container, services.SchedulingSettings{TolerateCordon: true}) {
		t.Error("client must tolerate cordon with SkipUnhealthyToleration=true")
	}
}
