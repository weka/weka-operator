package csi

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/weka/weka-operator/internal/services"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

// firstMatch is the toleration the taint manager applies to a NoExecute taint: the first one in list order.
func firstMatch(tols []corev1.Toleration, key string) (corev1.Toleration, int) {
	taint := &corev1.Taint{Key: key, Effect: corev1.TaintEffectNoExecute}
	i := slices.IndexFunc(tols, func(t corev1.Toleration) bool { return t.ToleratesTaint(klog.Background(), taint, false) })
	if i < 0 {
		return corev1.Toleration{}, -1
	}
	return tols[i], i
}

func seconds(t corev1.Toleration) int64 {
	if t.TolerationSeconds == nil {
		return -1
	}
	return *t.TolerationSeconds
}

func TestControllerTolerations(t *testing.T) {
	const maintenance = "example.com/maintenance"
	exists := corev1.Toleration{Operator: corev1.TolerationOpExists}
	noExecute := func(key string, secs int64) corev1.Toleration {
		return corev1.Toleration{Key: key, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &secs}
	}
	client := func(raw []corev1.Toleration, advanced ...corev1.Toleration) *weka.WekaClient {
		wc := testWekaClient(nil)
		wc.Spec.RawTolerations = raw
		if advanced != nil {
			wc.Spec.CsiConfig = &weka.ClientCsiConfig{Advanced: &weka.AdvancedCsiConfig{ControllerTolerations: advanced}}
		}
		return wc
	}
	on := services.SchedulingSettings{ClientsTolerateAll: true}

	tests := []struct {
		name          string
		wc            *weka.WekaClient
		scheduling    services.SchedulingSettings
		wantExists    int
		wantNotReady  int64 // tolerationSeconds of the first toleration matching the not-ready taint
		wantKept      []corev1.Toleration
		wantMaintains int64 // 0: not checked; else tolerationSeconds of the first match for another NoExecute taint
	}{
		{name: "flag off", wc: client(nil), wantNotReady: 10},
		{name: "flag on", wc: client(nil), scheduling: on, wantExists: 1, wantNotReady: 10},
		{name: "user Exists is not duplicated and does not outrank ours", wc: client([]corev1.Toleration{exists}), scheduling: on, wantExists: 1, wantNotReady: 10},
		{name: "user not-ready override takes the slot", wc: client([]corev1.Toleration{noExecute(corev1.TaintNodeNotReady, 300)}), wantNotReady: 300},
		{name: "user override wins over a user Exists", wc: client([]corev1.Toleration{exists}, noExecute(corev1.TaintNodeNotReady, 300)), wantExists: 1, wantNotReady: 300},
		{
			name:       "Equal toleration with a value cannot match the taint",
			wc:         client([]corev1.Toleration{{Key: corev1.TaintNodeNotReady, Operator: corev1.TolerationOpEqual, Value: "x", Effect: corev1.TaintEffectNoExecute}}),
			scheduling: on, wantExists: 1, wantNotReady: 10,
			wantKept: []corev1.Toleration{{Key: corev1.TaintNodeNotReady, Operator: corev1.TolerationOpEqual, Value: "x", Effect: corev1.TaintEffectNoExecute}},
		},
		{
			name:       "NoSchedule-only toleration cannot match the NoExecute taint",
			wc:         client([]corev1.Toleration{{Key: corev1.TaintNodeNotReady, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}),
			scheduling: on, wantExists: 1, wantNotReady: 10,
			wantKept: []corev1.Toleration{{Key: corev1.TaintNodeNotReady, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}},
		},
		{name: "the flag's Exists leads the user's other tolerations", wc: client([]corev1.Toleration{noExecute(maintenance, 30)}), scheduling: on, wantExists: 1, wantNotReady: 10, wantMaintains: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tols := controllerTolerations(tt.wc, tt.scheduling)
			if got := len(slices.DeleteFunc(slices.Clone(tols), func(tol corev1.Toleration) bool { return tol.Key != "" || tol.Effect != "" })); got != tt.wantExists {
				t.Errorf("keyless Exists appears %d times, want %d: %v", got, tt.wantExists, tols)
			}
			notReady, i := firstMatch(tols, corev1.TaintNodeNotReady)
			if seconds(notReady) != tt.wantNotReady {
				t.Errorf("first not-ready match (index %d) has tolerationSeconds %d, want %d: %v", i, seconds(notReady), tt.wantNotReady, tols)
			}
			if tt.wantNotReady == 10 && i > 1 {
				t.Errorf("our not-ready entry is at index %d, want it first or second: %v", i, tols)
			}
			if unreachable, _ := firstMatch(tols, corev1.TaintNodeUnreachable); seconds(unreachable) != 10 {
				t.Errorf("first unreachable match has tolerationSeconds %d, want 10: %v", seconds(unreachable), tols)
			}
			for _, kept := range tt.wantKept {
				if !slices.Contains(tols, kept) {
					t.Errorf("user toleration %v was dropped: %v", kept, tols)
				}
			}
			if tt.wantMaintains != 0 {
				if other, _ := firstMatch(tols, maintenance); seconds(other) != tt.wantMaintains {
					t.Errorf("first match for another taint has tolerationSeconds %d, want %d: %v", seconds(other), tt.wantMaintains, tols)
				}
			}
		})
	}
}

func TestControllerAffinityNotReadyTerm(t *testing.T) {
	podLabels := map[string]string{"app": "x"}
	if a := controllerAffinity(testWekaClient(nil), podLabels, services.SchedulingSettings{}); a != nil {
		t.Errorf("affinity without clientsTolerateAll: %v", a)
	}
	a := controllerAffinity(testWekaClient(nil), podLabels, services.SchedulingSettings{ClientsTolerateAll: true})
	terms := a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 1 || terms[0].MatchExpressions[0].Key != "weka.io/not-ready" || terms[0].MatchExpressions[0].Operator != corev1.NodeSelectorOpDoesNotExist {
		t.Errorf("terms = %v", terms)
	}

	nfs := testWekaClient(nil)
	nfs.Spec.UseNfs = true
	if a := controllerAffinity(nfs, podLabels, services.SchedulingSettings{ClientsTolerateAll: true}); a.PodAntiAffinity == nil || a.NodeAffinity == nil {
		t.Errorf("nfs affinity lost a part: %v", a)
	}
}

// With a user Exists the deduped tolerations are identical either way, so the hash must carry the flag itself.
func TestControllerHashChangesWithClientsTolerateAll(t *testing.T) {
	withUserExists := testWekaClient(nil)
	withUserExists.Spec.RawTolerations = []corev1.Toleration{{Operator: corev1.TolerationOpExists}}
	for name, wc := range map[string]*weka.WekaClient{"plain": testWekaClient(nil), "user Exists": withUserExists} {
		settings := services.DefaultConfigurationSettings().Csi
		off, err := GetCsiControllerDeploymentHash("csi", wc, settings, services.SchedulingSettings{})
		if err != nil {
			t.Fatal(err)
		}
		on, err := GetCsiControllerDeploymentHash("csi", wc, settings, services.SchedulingSettings{ClientsTolerateAll: true})
		if err != nil {
			t.Fatal(err)
		}
		if off == on {
			t.Errorf("%s: hash ignores clientsTolerateAll, so the controller would never be rolled", name)
		}
	}
}
