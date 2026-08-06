package operations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/internal/services/kubernetes"
)

// newFakeClient builds a controller-runtime fake client with the weka and corev1 schemes registered,
// seeded with objs. Call sites that need extra builder options (WithStatusSubresource,
// WithInterceptorFuncs, a wrapping client) build their own client.Client instead of using this helper.
func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("add weka scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

// withOperatorNamespace sets config.Config.OperatorPodNamespace for the duration of the test.
func withOperatorNamespace(t *testing.T, ns string) {
	t.Helper()
	config.Config.OperatorPodNamespace = ns
	t.Cleanup(func() { config.Config.OperatorPodNamespace = "" })
}

func TestMergeCampaignNodes(t *testing.T) {
	const targetImage = "registry/ssdproxy:v2"

	t.Run("resume preserves Phase/PreviousImage/StartedAt/Reason for a known in-flight node not yet on target", func(t *testing.T) {
		started := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		previous := []RotateSsdProxyNodeState{
			{
				Node:          "node-a",
				ProxyName:     "proxy-a",
				Phase:         RotateSsdProxyPhaseInFlight,
				PreviousImage: "registry/ssdproxy:v1",
				StartedAt:     &started,
				Reason:        "waiting for pod restart",
			},
		}
		targets := []targetProxy{
			{node: "node-a", container: weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "proxy-a"},
				Spec:       weka.WekaContainerSpec{Image: "registry/ssdproxy:v1"}, // pod hasn't restarted yet
			}},
		}

		got, dropped := mergeCampaignNodes(previous, targets, targetImage)
		if len(got) != 1 {
			t.Fatalf("expected 1 node, got %d: %+v", len(got), got)
		}
		if len(dropped) != 0 {
			t.Errorf("expected no dropped nodes, got %+v", dropped)
		}
		n := got[0]
		if n.Phase != RotateSsdProxyPhaseInFlight {
			t.Errorf("Phase = %q, want %q", n.Phase, RotateSsdProxyPhaseInFlight)
		}
		if n.PreviousImage != "registry/ssdproxy:v1" {
			t.Errorf("PreviousImage = %q, want preserved", n.PreviousImage)
		}
		if n.StartedAt == nil || !n.StartedAt.Equal(&started) {
			t.Errorf("StartedAt = %v, want preserved %v", n.StartedAt, started)
		}
		if n.Reason != "waiting for pod restart" {
			t.Errorf("Reason = %q, want preserved", n.Reason)
		}
	})

	t.Run("newly-appeared node is added as Pending", func(t *testing.T) {
		targets := []targetProxy{
			{node: "node-new", container: weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "proxy-new"},
				Spec:       weka.WekaContainerSpec{Image: "registry/ssdproxy:v1"},
			}},
		}
		got, _ := mergeCampaignNodes(nil, targets, targetImage)
		if len(got) != 1 {
			t.Fatalf("expected 1 node, got %d", len(got))
		}
		if got[0].Phase != RotateSsdProxyPhasePending {
			t.Errorf("Phase = %q, want %q", got[0].Phase, RotateSsdProxyPhasePending)
		}
		if got[0].ProxyName != "proxy-new" || got[0].Image != "registry/ssdproxy:v1" {
			t.Errorf("unexpected node state: %+v", got[0])
		}
	})

	t.Run("node that no longer has a proxy is dropped", func(t *testing.T) {
		previous := []RotateSsdProxyNodeState{
			{Node: "node-gone", ProxyName: "proxy-gone", Phase: RotateSsdProxyPhasePending},
			{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhasePending},
		}
		targets := []targetProxy{
			{node: "node-a", container: weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "proxy-a"},
				Spec:       weka.WekaContainerSpec{Image: "registry/ssdproxy:v1"},
			}},
		}
		got, dropped := mergeCampaignNodes(previous, targets, targetImage)
		if len(got) != 1 {
			t.Fatalf("expected node-gone to be dropped, got %d nodes: %+v", len(got), got)
		}
		if got[0].Node != "node-a" {
			t.Errorf("unexpected surviving node %q", got[0].Node)
		}
		// node-gone was Pending, so its disappearance is unremarkable (it never started) and must
		// not be reported for an event -- only InFlight/Done drops are.
		if len(dropped) != 0 {
			t.Errorf("expected a dropped Pending node to be silent (not reported), got %+v", dropped)
		}
	})

	t.Run("a dropped InFlight or Done node is reported so Plan can warn about it", func(t *testing.T) {
		previous := []RotateSsdProxyNodeState{
			{Node: "node-inflight", ProxyName: "proxy-inflight", Phase: RotateSsdProxyPhaseInFlight, PreviousImage: "registry/ssdproxy:v1"},
			{Node: "node-done", ProxyName: "proxy-done", Phase: RotateSsdProxyPhaseDone, PreviousImage: "registry/ssdproxy:v1", Image: targetImage},
			{Node: "node-pending", ProxyName: "proxy-pending", Phase: RotateSsdProxyPhasePending},
			{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhasePending},
		}
		targets := []targetProxy{
			{node: "node-a", container: weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "proxy-a"},
				Spec:       weka.WekaContainerSpec{Image: "registry/ssdproxy:v1"},
			}},
		}
		got, dropped := mergeCampaignNodes(previous, targets, targetImage)
		if len(got) != 1 || got[0].Node != "node-a" {
			t.Fatalf("expected only node-a to survive, got %+v", got)
		}
		if len(dropped) != 2 {
			t.Fatalf("expected 2 dropped nodes (InFlight + Done), got %d: %+v", len(dropped), dropped)
		}
		droppedByNode := map[weka.NodeName]RotateSsdProxyNodeState{}
		for _, n := range dropped {
			droppedByNode[n.Node] = n
		}
		if n, ok := droppedByNode["node-inflight"]; !ok || n.Phase != RotateSsdProxyPhaseInFlight {
			t.Errorf("expected node-inflight to be reported dropped with Phase InFlight, got %+v (present=%v)", n, ok)
		}
		if n, ok := droppedByNode["node-done"]; !ok || n.Phase != RotateSsdProxyPhaseDone {
			t.Errorf("expected node-done to be reported dropped with Phase Done, got %+v (present=%v)", n, ok)
		}
		if _, ok := droppedByNode["node-pending"]; ok {
			t.Errorf("expected node-pending to NOT be reported dropped (it never started)")
		}
	})

	t.Run("a proxy already on target image is marked Skipped even with no prior state", func(t *testing.T) {
		targets := []targetProxy{
			{node: "node-a", container: weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "proxy-a"},
				Spec:       weka.WekaContainerSpec{Image: targetImage},
			}},
		}
		got, _ := mergeCampaignNodes(nil, targets, targetImage)
		if len(got) != 1 {
			t.Fatalf("expected 1 node, got %d", len(got))
		}
		if got[0].Phase != RotateSsdProxyPhaseSkipped {
			t.Errorf("Phase = %q, want %q", got[0].Phase, RotateSsdProxyPhaseSkipped)
		}
		if got[0].Reason != "" {
			t.Errorf("Reason = %q, want cleared on Skipped", got[0].Reason)
		}
	})

	t.Run("an on-target proxy this campaign never patched is (re)marked Skipped, clearing Reason and BlockedSince", func(t *testing.T) {
		// e.g. a hand-rollback of an InFlight node must land as Skipped on the next merge, not stuck
		// InFlight forever.
		blockedSince := metav1.NewTime(time.Now().Add(-20 * time.Minute))
		previous := []RotateSsdProxyNodeState{
			{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhaseInFlight, Reason: "waiting for pod restart", BlockedSince: &blockedSince},
		}
		targets := []targetProxy{
			{node: "node-a", container: weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "proxy-a"},
				Spec:       weka.WekaContainerSpec{Image: targetImage},
			}},
		}
		got, _ := mergeCampaignNodes(previous, targets, targetImage)
		if got[0].Phase != RotateSsdProxyPhaseSkipped {
			t.Errorf("Phase = %q, want %q", got[0].Phase, RotateSsdProxyPhaseSkipped)
		}
		if got[0].Reason != "" {
			t.Errorf("Reason = %q, want cleared", got[0].Reason)
		}
		if got[0].BlockedSince != nil {
			t.Errorf("BlockedSince = %v, want cleared on Skipped override", got[0].BlockedSince)
		}
	})

	// Regression test for the verification-bypass defect: advancePending patches spec.image and marks
	// the node InFlight in one pass, so from the next reconcile spec.image ALREADY equals the target
	// while the pod is still being recreated. If the already-on-target rule fires unconditionally it
	// clears InFlight, advanceInFlight (pod image + Running + READY + VerifyNodeRecovered) never runs,
	// no NodeComplete event fires, and the next Pending node is patched immediately — proxies then
	// restart concurrently with no recovery gate. PreviousImage != "" is the discriminator.
	t.Run("a node THIS campaign patched stays InFlight even though spec.image already equals the target", func(t *testing.T) {
		started := metav1.NewTime(time.Now().Add(-20 * time.Second))
		previous := []RotateSsdProxyNodeState{
			{
				Node:          "node-a",
				ProxyName:     "proxy-a",
				Phase:         RotateSsdProxyPhaseInFlight,
				PreviousImage: "registry/ssdproxy:v1",
				StartedAt:     &started,
			},
		}
		targets := []targetProxy{
			{node: "node-a", container: weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "proxy-a"},
				// The CR spec is patched instantly; only the pod lags behind.
				Spec: weka.WekaContainerSpec{Image: targetImage},
			}},
		}
		got, _ := mergeCampaignNodes(previous, targets, targetImage)
		if got[0].Phase != RotateSsdProxyPhaseInFlight {
			t.Errorf("Phase = %q, want %q — the InFlight marker must survive so advanceInFlight can run the post-rotation checks", got[0].Phase, RotateSsdProxyPhaseInFlight)
		}
		if got[0].StartedAt == nil || !got[0].StartedAt.Equal(&started) {
			t.Errorf("StartedAt = %v, want preserved %v (drives the stuck-warning timer)", got[0].StartedAt, started)
		}
	})

	t.Run("a Done node this campaign rotated stays Done on re-merge and is not downgraded", func(t *testing.T) {
		// Terminal-phase stability. countDoneOrSkipped treats Done and Skipped alike so progress is
		// unaffected either way, but Done carries the "we rotated it" information Skipped loses.
		previous := []RotateSsdProxyNodeState{
			{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhaseDone, PreviousImage: "registry/ssdproxy:v1", Image: targetImage},
		}
		targets := []targetProxy{
			{node: "node-a", container: weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "proxy-a"},
				Spec:       weka.WekaContainerSpec{Image: targetImage},
			}},
		}
		got, _ := mergeCampaignNodes(previous, targets, targetImage)
		if got[0].Phase != RotateSsdProxyPhaseDone {
			t.Errorf("Phase = %q, want preserved %q", got[0].Phase, RotateSsdProxyPhaseDone)
		}
	})

	t.Run("a Done node whose image no longer equals the target stays Done and is not reset", func(t *testing.T) {
		// Simulates a hand-rollback after completion: the live proxy's image no longer equals the
		// campaign's target, so the Skipped idempotency check does not fire, and nothing else in
		// mergeCampaignNodes resets a known node's Phase.
		previous := []RotateSsdProxyNodeState{
			{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhaseDone, Image: targetImage},
		}
		targets := []targetProxy{
			{node: "node-a", container: weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "proxy-a"},
				Spec:       weka.WekaContainerSpec{Image: "registry/ssdproxy:rolled-back"},
			}},
		}
		got, _ := mergeCampaignNodes(previous, targets, targetImage)
		if got[0].Phase != RotateSsdProxyPhaseDone {
			t.Errorf("Phase = %q, want preserved %q", got[0].Phase, RotateSsdProxyPhaseDone)
		}
	})

	t.Run("a Skipped node whose proxy has drifted off the target image reverts to Pending", func(t *testing.T) {
		// e.g. the ssdproxy CR was deleted and recreated on the operator's configured image, which can
		// differ from this campaign's target. Skipped is a claim about the CURRENT image, so it must
		// not survive the drift, or the node is silently never re-rotated.
		previous := []RotateSsdProxyNodeState{
			{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhaseSkipped},
		}
		targets := []targetProxy{
			{node: "node-a", container: weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "proxy-a"},
				Spec:       weka.WekaContainerSpec{Image: "registry/ssdproxy:drifted"},
			}},
		}
		got, _ := mergeCampaignNodes(previous, targets, targetImage)
		if got[0].Phase != RotateSsdProxyPhasePending {
			t.Errorf("Phase = %q, want %q", got[0].Phase, RotateSsdProxyPhasePending)
		}
	})

	t.Run("an InFlight node whose proxy is off the target image is left InFlight, not reset by the Skipped drift rule", func(t *testing.T) {
		previous := []RotateSsdProxyNodeState{
			{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhaseInFlight, Reason: "waiting for pod restart"},
		}
		targets := []targetProxy{
			{node: "node-a", container: weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "proxy-a"},
				Spec:       weka.WekaContainerSpec{Image: "registry/ssdproxy:drifted"},
			}},
		}
		got, _ := mergeCampaignNodes(previous, targets, targetImage)
		if got[0].Phase != RotateSsdProxyPhaseInFlight {
			t.Errorf("Phase = %q, want preserved %q", got[0].Phase, RotateSsdProxyPhaseInFlight)
		}
	})

}

// TestParkedWarnSignal_ShouldWarn covers shouldWarn's elapsed-time boundary logic for all four
// parkedWarnSignal instances. It is a pure predicate over elapsed time -- it must never influence
// which phase a node is in; it only decides whether to (re)emit a throttled Warning event.
func TestParkedWarnSignal_ShouldWarn(t *testing.T) {
	signals := []struct {
		name   string
		signal parkedWarnSignal
	}{
		{"blockedWarnSignal", blockedWarnSignal},
		{"stuckWarnSignal", stuckWarnSignal},
		{"campaignParkedWarnSignal", campaignParkedWarnSignal},
		{"campaignParkedInFlightWarnSignal", campaignParkedInFlightWarnSignal},
	}
	for _, s := range signals {
		t.Run(s.name, func(t *testing.T) {
			threshold, repeat := s.signal.threshold, s.signal.repeat
			cases := []struct {
				name    string
				elapsed time.Duration
				want    bool
			}{
				{"well before threshold -> false", threshold / 3, false},
				{"just before threshold -> false", threshold - time.Second, false},
				{"exactly at threshold -> true", threshold, true},
				{"just past threshold, within window -> true", threshold + 10*time.Second, true},
				{"past threshold but outside window (between warnings) -> false", threshold + repeat/2, false},
				{"at next repeat boundary -> true", threshold + repeat, true},
				{"just past next repeat boundary -> true", threshold + repeat + 5*time.Second, true},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					if got := s.signal.shouldWarn(tc.elapsed); got != tc.want {
						t.Errorf("shouldWarn(%s) = %v, want %v", tc.elapsed, got, tc.want)
					}
				})
			}
		})
	}
}

func TestPodAndProxyReady(t *testing.T) {
	const targetImage = "registry/ssdproxy:v2"

	readyPod := func(image string) *corev1.Pod {
		return &corev1.Pod{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: consts.WekaContainerName, Image: image}},
			},
		}
	}

	t.Run("all conditions satisfied -> ready", func(t *testing.T) {
		proxy := &weka.WekaContainer{Status: weka.WekaContainerStatus{Status: weka.Running, InternalStatus: "READY"}}
		ready, reason, err := podAndProxyReady(proxy, readyPod(targetImage), targetImage)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ready || reason != "" {
			t.Errorf("ready=%v reason=%q, want ready with no reason", ready, reason)
		}
	})

	failCases := []struct {
		name       string
		proxy      *weka.WekaContainer
		podImage   string
		wantReason string
	}{
		{
			name:       "pod image mismatch blocks and names the check",
			proxy:      &weka.WekaContainer{Status: weka.WekaContainerStatus{Status: weka.Running, InternalStatus: "READY"}},
			podImage:   "registry/ssdproxy:v1",
			wantReason: "not yet recreated on target image",
		},
		{
			name:       "proxy Status.Status != Running blocks and names the check",
			proxy:      &weka.WekaContainer{Status: weka.WekaContainerStatus{Status: weka.PodRunning, InternalStatus: "READY"}},
			podImage:   targetImage,
			wantReason: "proxy status is",
		},
		{
			name:       "proxy InternalStatus != READY blocks and names the check",
			proxy:      &weka.WekaContainer{Status: weka.WekaContainerStatus{Status: weka.Running, InternalStatus: "STARTING"}},
			podImage:   targetImage,
			wantReason: "proxy internal status is",
		},
	}
	for _, tc := range failCases {
		t.Run(tc.name, func(t *testing.T) {
			ready, reason, err := podAndProxyReady(tc.proxy, readyPod(tc.podImage), targetImage)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ready {
				t.Errorf("expected not ready")
			}
			if !strings.Contains(reason, tc.wantReason) {
				t.Errorf("reason = %q, want it to contain %q", reason, tc.wantReason)
			}
		})
	}

	t.Run("regression guard: LastAppliedImage already == target must not substitute for a broken pod", func(t *testing.T) {
		// flow_active_state.go exempts service/ssdproxy containers from the READY+lease gate before
		// stamping LastAppliedImage, so LastAppliedImage is not a trustworthy readiness signal for an
		// ssdproxy container. podAndProxyReady must derive readiness solely from the live pod/proxy
		// state and must never consult Status.LastAppliedImage.
		proxy := &weka.WekaContainer{
			Status: weka.WekaContainerStatus{
				Status:           weka.PodRunning, // not actually Running
				InternalStatus:   "STARTING",      // not actually READY
				LastAppliedImage: targetImage,     // already stamped, but must not be trusted
			},
		}
		ready, reason, err := podAndProxyReady(proxy, readyPod("registry/ssdproxy:v1"), targetImage)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ready {
			t.Errorf("expected blocked despite LastAppliedImage matching target")
		}
		if reason == "" {
			t.Errorf("expected a reason naming the actual failing check")
		}
	})

	t.Run("no weka container in pod -> error", func(t *testing.T) {
		proxy := &weka.WekaContainer{Status: weka.WekaContainerStatus{Status: weka.Running, InternalStatus: "READY"}}
		if _, _, err := podAndProxyReady(proxy, &corev1.Pod{}, targetImage); err == nil {
			t.Errorf("expected an error when the pod has no weka container")
		}
	})
}

// Regression tests for the "Plan failures are invisible on the CR" defect: Plan recorded its error into
// o.results.Err and returned it, but no callback ran — so status.status and status.result stayed null
// forever, no event fired, and the op hot-looped with the reason only in operator logs.
func TestPlanErrorsReachTheOwnerStatus(t *testing.T) {
	newOp := func(owner *weka.WekaManualOperation, failed, progressed *int) *RotateSsdProxyOperation {
		return &RotateSsdProxyOperation{
			ownerRef:         owner,
			failureCallback:  func(context.Context) error { *failed++; return nil },
			progressCallback: func(context.Context) error { *progressed++; return nil },
		}
	}
	asWaitError := func(t *testing.T, err error) *lifecycle.WaitError {
		t.Helper()
		var we *lifecycle.WaitError
		if !errors.As(err, &we) {
			t.Fatalf("expected a lifecycle.WaitError so the engine requeues instead of hot-looping, got %T: %v", err, err)
		}
		return we
	}

	t.Run("terminal error marks the owner Failed and lands in the JSON result", func(t *testing.T) {
		var failed, progressed int
		op := newOp(&weka.WekaManualOperation{}, &failed, &progressed)

		err := op.failTerminally(context.Background(), errors.New("no target image: set spec.payload..."))

		asWaitError(t, err)
		if failed != 1 {
			t.Errorf("failureCallback calls = %d, want 1 (this is what writes status.status=Failed)", failed)
		}
		if progressed != 0 {
			t.Errorf("progressCallback calls = %d, want 0 for a terminal error", progressed)
		}
		if op.results.Err == "" {
			t.Fatal("results.Err not set")
		}
		// The reason must survive serialization into status.result — the only place a human reads it.
		var decoded RotateSsdProxyResult
		if uerr := json.Unmarshal([]byte(op.GetJsonResult()), &decoded); uerr != nil {
			t.Fatalf("GetJsonResult did not produce valid JSON: %v", uerr)
		}
		if !strings.Contains(decoded.Err, "no target image") {
			t.Errorf("status.result.err = %q, want it to name the failure", decoded.Err)
		}
	})

	t.Run("terminal error with an InFlight node emits a Warning event so it isn't silently abandoned", func(t *testing.T) {
		var failed, progressed int
		recorder := record.NewFakeRecorder(10)
		op := newOp(&weka.WekaManualOperation{}, &failed, &progressed)
		op.recorder = recorder
		op.results.Nodes = []RotateSsdProxyNodeState{
			{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhaseInFlight, PreviousImage: "registry/ssdproxy:v1", Image: "registry/ssdproxy:v2"},
		}

		if err := op.failTerminally(context.Background(), errors.New("no target image")); err == nil {
			t.Fatal("expected an error back")
		}

		select {
		case event := <-recorder.Events:
			if !strings.Contains(event, "node-a") {
				t.Errorf("event = %q, want it to name the abandoned node", event)
			}
		default:
			t.Error("expected a Warning event naming the abandoned InFlight node, got none")
		}
	})

	t.Run("terminal error with no InFlight node emits no event", func(t *testing.T) {
		var failed, progressed int
		recorder := record.NewFakeRecorder(10)
		op := newOp(&weka.WekaManualOperation{}, &failed, &progressed)
		op.recorder = recorder
		op.results.Nodes = []RotateSsdProxyNodeState{
			{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhaseDone},
		}

		if err := op.failTerminally(context.Background(), errors.New("no target image")); err == nil {
			t.Fatal("expected an error back")
		}

		select {
		case event := <-recorder.Events:
			t.Errorf("expected no event when nothing is InFlight, got: %q", event)
		default:
		}
	})

	t.Run("an already-Failed owner is not rewritten every reconcile, and the InFlight event does not refire", func(t *testing.T) {
		var failed, progressed int
		recorder := record.NewFakeRecorder(10)
		op := newOp(&weka.WekaManualOperation{Status: weka.WekaManualOperationStatus{Status: "Failed"}}, &failed, &progressed)
		op.recorder = recorder
		op.results.Nodes = []RotateSsdProxyNodeState{
			{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhaseInFlight},
		}

		err := op.failTerminally(context.Background(), errors.New("no target image"))

		asWaitError(t, err)
		if failed != 0 {
			t.Errorf("failureCallback calls = %d, want 0 once the owner is already Failed", failed)
		}
		if op.results.Err == "" {
			t.Error("results.Err should still be refreshed even when the status write is skipped")
		}
		// Same guard as the status write, deliberately: an unguarded Eventf here would refire the
		// identical "abandoned node" message every ~15s forever once the owner is already Failed.
		select {
		case event := <-recorder.Events:
			t.Errorf("expected no repeated event once the owner is already Failed, got: %q", event)
		default:
		}
	})

	t.Run("transient error persists via progress and leaves the owner un-Failed", func(t *testing.T) {
		// A concurrent campaign clears by itself, so this must NOT mark the owner Failed: onProgress
		// only promotes ""->Running and never clears Failed, wedging a campaign that could have run.
		var failed, progressed int
		op := newOp(&weka.WekaManualOperation{}, &failed, &progressed)

		err := op.waitWithPersistedErr(context.Background(),
			errors.New("another rotate-ssdproxy operation (ns/other) is already Running; only one may run at a time"))

		asWaitError(t, err)
		if failed != 0 {
			t.Errorf("failureCallback calls = %d, want 0 for a transient error", failed)
		}
		if progressed != 1 {
			t.Errorf("progressCallback calls = %d, want 1", progressed)
		}
		if !strings.Contains(op.results.Err, "already Running") {
			t.Errorf("results.Err = %q, want it to name the conflicting campaign", op.results.Err)
		}
	})

	t.Run("BlockedSince is stamped once and not refreshed on a second park", func(t *testing.T) {
		var failed, progressed int
		op := newOp(&weka.WekaManualOperation{}, &failed, &progressed)

		if err := op.waitWithPersistedErr(context.Background(), errors.New("first park reason")); err == nil {
			t.Fatal("expected a WaitError")
		}
		if op.results.BlockedSince == nil {
			t.Fatal("expected BlockedSince to be stamped on the first park")
		}
		first := *op.results.BlockedSince

		// A different reason on the very next cycle must not push BlockedSince forward — it tracks how
		// long the campaign has been continuously blocked, not how long the CURRENT reason has held.
		if err := op.waitWithPersistedErr(context.Background(), errors.New("second, unrelated park reason")); err == nil {
			t.Fatal("expected a WaitError")
		}
		if op.results.BlockedSince == nil || !op.results.BlockedSince.Time.Equal(first.Time) {
			t.Errorf("BlockedSince changed across parks: first=%v, second=%v", first, op.results.BlockedSince)
		}
		if !strings.Contains(op.results.Err, "second, unrelated park reason") {
			t.Errorf("results.Err = %q, want the latest reason even though BlockedSince didn't move", op.results.Err)
		}
	})

	t.Run("a campaign-scope park with an InFlight node emits Stalled naming that node, not the generic Blocked message", func(t *testing.T) {
		// refuseIfAnotherCampaignRunning fires from Plan, before AdvanceOne runs, so it can catch a node
		// already InFlight from a previous cycle. That node is patched but unverified, which is active
		// impact like a stuck node -- so it must use the Stuck thresholds/reason and name the node.
		recorder := record.NewFakeRecorder(10)
		op := newOp(&weka.WekaManualOperation{}, new(int), new(int))
		op.recorder = recorder
		op.results.Nodes = []RotateSsdProxyNodeState{
			{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhaseInFlight},
		}
		// 5m (Stuck threshold) + 10s lands inside the 30s warn window; shouldWarn is modulo-repeat, so
		// don't just clear the threshold -- land inside the window or it silently doesn't fire.
		blockedSince := metav1.NewTime(time.Now().Add(-(5*time.Minute + 10*time.Second)))
		op.results.BlockedSince = &blockedSince

		if err := op.waitWithPersistedErr(context.Background(), errors.New("another campaign is already Running")); err == nil {
			t.Fatal("expected a WaitError")
		}

		select {
		case event := <-recorder.Events:
			if !strings.Contains(event, rotateSsdProxyEventReasonStalled) {
				t.Errorf("event = %q, want reason %q", event, rotateSsdProxyEventReasonStalled)
			}
			if !strings.Contains(event, "node-a") {
				t.Errorf("event = %q, want it to name the in-flight node", event)
			}
		default:
			t.Error("expected a Warning event naming the in-flight node, got none")
		}
	})

	t.Run("a campaign-scope park with no InFlight node emits the generic Blocked message", func(t *testing.T) {
		recorder := record.NewFakeRecorder(10)
		op := newOp(&weka.WekaManualOperation{}, new(int), new(int))
		op.recorder = recorder
		// Same window rule as above: 15m (Blocked threshold) + 10s, not just "past the threshold".
		blockedSince := metav1.NewTime(time.Now().Add(-(15*time.Minute + 10*time.Second)))
		op.results.BlockedSince = &blockedSince

		if err := op.waitWithPersistedErr(context.Background(), errors.New("nodeSelector matched nothing")); err == nil {
			t.Fatal("expected a WaitError")
		}

		select {
		case event := <-recorder.Events:
			if !strings.Contains(event, rotateSsdProxyEventReasonBlocked) {
				t.Errorf("event = %q, want reason %q", event, rotateSsdProxyEventReasonBlocked)
			}
			if !strings.Contains(event, "blocked before any node could be targeted") {
				t.Errorf("event = %q, want the generic campaign-parked message", event)
			}
		default:
			t.Error("expected a Warning event with the generic campaign-parked message, got none")
		}
	})

	t.Run("nil callbacks do not panic", func(t *testing.T) {
		op := &RotateSsdProxyOperation{ownerRef: &weka.WekaManualOperation{}}
		if err := op.failTerminally(context.Background(), errors.New("boom")); err == nil {
			t.Error("expected an error back")
		}
		if err := op.waitWithPersistedErr(context.Background(), errors.New("boom")); err == nil {
			t.Error("expected an error back")
		}
	})
}

// rotateSsdProxyTestManager is a minimal ctrl.Manager stand-in (same embed-and-override pattern as
// traceSessionTestManager in trace_session_test.go). Plan only calls GetAPIReader(); every other
// method panics on the embedded nil interface, which is the point.
type rotateSsdProxyTestManager struct {
	ctrl.Manager
	reader client.Reader
}

func (m *rotateSsdProxyTestManager) GetAPIReader() client.Reader {
	return m.reader
}

// fakeSsdProxyKubeService covers the only two methods Plan's path reaches, via resolveTargetProxies.
// The rest panic, same rationale as above.
type fakeSsdProxyKubeService struct {
	kubernetes.KubeService
	containers    []weka.WekaContainer
	containersErr error
	nodes         []corev1.Node
	nodesErr      error
}

func (f *fakeSsdProxyKubeService) GetWekaContainersSimple(_ context.Context, _, _ string, _ map[string]string) ([]weka.WekaContainer, error) {
	return f.containers, f.containersErr
}

func (f *fakeSsdProxyKubeService) GetNodes(_ context.Context, _ map[string]string) ([]corev1.Node, error) {
	return f.nodes, f.nodesErr
}

// newPlanTestManager backs the API reader with an empty fake client, so
// refuseIfAnotherCampaignRunning sees no conflicting campaign.
func newPlanTestManager(t *testing.T) ctrl.Manager {
	t.Helper()
	return &rotateSsdProxyTestManager{reader: newFakeClient(t)}
}

// fakeGate stubs the RotateSsdProxyOperation.gate field, letting tests drive
// evaluateGate/advancePending/advanceInFlight through the gate's allowed/blocked/error outcomes
// directly. EvaluateNodeDisruption itself is not reachable through controller-runtime fakes alone —
// its second discovery source needs a real Secret (node agent token) and a real HTTP call
// (ListVirtualDrives) — so this is the seam the gate field exists for (see its doc comment on
// RotateSsdProxyOperation). mgr/execSvc are intentionally unused and safe to leave nil in these
// tests: the fake never touches them.
func fakeGate(verdicts []ClusterVerdict, err error) func(context.Context, ctrl.Manager, exec.ExecService, weka.NodeName, *weka.WekaContainer) ([]ClusterVerdict, error) {
	return func(context.Context, ctrl.Manager, exec.ExecService, weka.NodeName, *weka.WekaContainer) ([]ClusterVerdict, error) {
		return verdicts, err
	}
}

// TestPlanEarlyReturnsPreservePriorCampaignState covers Plan's four early-return paths (missing
// image, another campaign running, resolveTargetProxies error, unmatched nodeSelector). Zero targets
// for a non-empty NodeSelector must park naming the selector, not fall through to a "0 nodes"
// CampaignComplete indistinguishable from a real rollout. rehydrateFrom now runs unconditionally as
// Plan's very first statement, so every one of these paths must come out the other side with
// the prior campaign's Nodes/Total/Done — including every node's PreviousImage — untouched.
func TestPlanEarlyReturnsPreservePriorCampaignState(t *testing.T) {
	withOperatorNamespace(t, "test-ns")

	previous := RotateSsdProxyResult{
		TargetImage: "registry/ssdproxy:v2",
		Total:       2,
		Done:        1,
		Nodes: []RotateSsdProxyNodeState{
			{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhaseDone, PreviousImage: "registry/ssdproxy:v1", Image: "registry/ssdproxy:v2"},
			{Node: "node-b", ProxyName: "proxy-b", Phase: RotateSsdProxyPhasePending},
		},
	}
	previousJSON, err := json.Marshal(previous)
	if err != nil {
		t.Fatalf("marshal previous result: %v", err)
	}

	newOwner := func() *weka.WekaManualOperation {
		return &weka.WekaManualOperation{
			ObjectMeta: metav1.ObjectMeta{UID: "self-uid"},
			Spec:       weka.WekaManualOperationSpec{Action: weka.WekaManualOperationActionRotateSsdProxy},
			Status:     weka.WekaManualOperationStatus{Result: string(previousJSON)},
		}
	}

	assertPreserved := func(t *testing.T, op *RotateSsdProxyOperation) {
		t.Helper()
		if op.results.Total != previous.Total {
			t.Errorf("results.Total = %d, want preserved %d", op.results.Total, previous.Total)
		}
		if op.results.Done != previous.Done {
			t.Errorf("results.Done = %d, want preserved %d", op.results.Done, previous.Done)
		}
		if len(op.results.Nodes) != len(previous.Nodes) {
			t.Fatalf("results.Nodes has %d entries, want %d preserved", len(op.results.Nodes), len(previous.Nodes))
		}
		for i, n := range op.results.Nodes {
			if n.PreviousImage != previous.Nodes[i].PreviousImage {
				t.Errorf("Nodes[%d].PreviousImage = %q, want preserved %q", i, n.PreviousImage, previous.Nodes[i].PreviousImage)
			}
		}
	}

	t.Run("missing target image fails terminally", func(t *testing.T) {
		var failed int
		op := &RotateSsdProxyOperation{
			mgr:              newPlanTestManager(t),
			ownerRef:         newOwner(),
			payload:          &weka.RotateSsdProxyPayload{}, // no targetImage, no override configured
			kubeService:      &fakeSsdProxyKubeService{},
			progressCallback: func(context.Context) error { return nil },
			failureCallback:  func(context.Context) error { failed++; return nil },
		}
		planErr := op.Plan(context.Background())
		var we *lifecycle.WaitError
		if !errors.As(planErr, &we) {
			t.Fatalf("expected a lifecycle.WaitError, got %T: %v", planErr, planErr)
		}
		if failed != 1 {
			t.Errorf("failureCallback calls = %d, want 1", failed)
		}
		assertPreserved(t, op)
	})

	t.Run("another campaign running parks", func(t *testing.T) {
		other := &weka.WekaManualOperation{
			ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "ns", UID: "other-uid"},
			Spec:       weka.WekaManualOperationSpec{Action: weka.WekaManualOperationActionRotateSsdProxy},
			Status:     weka.WekaManualOperationStatus{Status: "Running"},
		}
		fakeClient := newFakeClient(t, other)
		var progressed int
		op := &RotateSsdProxyOperation{
			mgr:              &rotateSsdProxyTestManager{reader: fakeClient},
			ownerRef:         newOwner(),
			payload:          &weka.RotateSsdProxyPayload{TargetImage: "registry/ssdproxy:v2"},
			kubeService:      &fakeSsdProxyKubeService{},
			progressCallback: func(context.Context) error { progressed++; return nil },
		}
		planErr := op.Plan(context.Background())
		var we *lifecycle.WaitError
		if !errors.As(planErr, &we) {
			t.Fatalf("expected a lifecycle.WaitError (park), got %T: %v", planErr, planErr)
		}
		if !strings.Contains(planErr.Error(), "other") {
			t.Errorf("error = %q, want it to name the conflicting campaign", planErr.Error())
		}
		if progressed != 1 {
			t.Errorf("progressCallback calls = %d, want 1", progressed)
		}
		assertPreserved(t, op)
	})

	t.Run("resolveTargetProxies error parks", func(t *testing.T) {
		var progressed int
		op := &RotateSsdProxyOperation{
			mgr:              newPlanTestManager(t),
			ownerRef:         newOwner(),
			payload:          &weka.RotateSsdProxyPayload{TargetImage: "registry/ssdproxy:v2"},
			kubeService:      &fakeSsdProxyKubeService{containersErr: errors.New("list failed")},
			progressCallback: func(context.Context) error { progressed++; return nil },
		}
		planErr := op.Plan(context.Background())
		var we *lifecycle.WaitError
		if !errors.As(planErr, &we) {
			t.Fatalf("expected a lifecycle.WaitError (park), got %T: %v", planErr, planErr)
		}
		if progressed != 1 {
			t.Errorf("progressCallback calls = %d, want 1", progressed)
		}
		assertPreserved(t, op)
	})

	t.Run("nodeSelector matching nothing parks", func(t *testing.T) {
		var progressed int
		op := &RotateSsdProxyOperation{
			mgr:      newPlanTestManager(t),
			ownerRef: newOwner(),
			payload: &weka.RotateSsdProxyPayload{
				TargetImage:  "registry/ssdproxy:v2",
				NodeSelector: map[string]string{"typo-label": "true"},
			},
			kubeService:      &fakeSsdProxyKubeService{},
			progressCallback: func(context.Context) error { progressed++; return nil },
		}
		planErr := op.Plan(context.Background())
		var we *lifecycle.WaitError
		if !errors.As(planErr, &we) {
			t.Fatalf("expected a lifecycle.WaitError (park), got %T: %v", planErr, planErr)
		}
		if !strings.Contains(planErr.Error(), "nodeSelector") || !strings.Contains(planErr.Error(), "matched no nodes") {
			t.Errorf("error = %q, want it to name the unmatched nodeSelector", planErr.Error())
		}
		if progressed != 1 {
			t.Errorf("progressCallback calls = %d, want 1", progressed)
		}
		if !strings.Contains(op.results.Err, "nodeSelector") {
			t.Errorf("results.Err = %q, want it to name the unmatched nodeSelector", op.results.Err)
		}
		assertPreserved(t, op)
	})
}

// TestPlanFailsTerminallyWhenResolvedTargetImageChanges covers a resolved-target-image race: once a campaign has a node in
// flight, the operator-wide helm override can still change the resolved target image out from under
// it even though payload.targetImage itself is immutable post-creation (enforced separately by a CEL
// marker on the CRD). That must fail terminally, exactly once, and must not discard the campaign's
// prior node history — only deleting the WekaManualOperation and starting a new one recovers.
func TestPlanFailsTerminallyWhenResolvedTargetImageChanges(t *testing.T) {
	withOperatorNamespace(t, "test-ns")

	previous := RotateSsdProxyResult{
		TargetImage: "registry/ssdproxy:v1",
		Total:       2,
		Done:        1,
		Nodes: []RotateSsdProxyNodeState{
			{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhaseDone, PreviousImage: "registry/ssdproxy:v0", Image: "registry/ssdproxy:v1"},
			{Node: "node-b", ProxyName: "proxy-b", Phase: RotateSsdProxyPhaseInFlight, PreviousImage: "registry/ssdproxy:v0"},
		},
	}
	previousJSON, err := json.Marshal(previous)
	if err != nil {
		t.Fatalf("marshal previous result: %v", err)
	}

	var failed, progressed int
	op := &RotateSsdProxyOperation{
		mgr: newPlanTestManager(t),
		ownerRef: &weka.WekaManualOperation{
			ObjectMeta: metav1.ObjectMeta{UID: "self-uid"},
			Status:     weka.WekaManualOperationStatus{Result: string(previousJSON)},
		},
		// Changed from v1 (what the in-flight campaign started with) to v2.
		payload:          &weka.RotateSsdProxyPayload{TargetImage: "registry/ssdproxy:v2"},
		kubeService:      &fakeSsdProxyKubeService{},
		progressCallback: func(context.Context) error { progressed++; return nil },
		failureCallback:  func(context.Context) error { failed++; return nil },
	}

	planErr := op.Plan(context.Background())

	var we *lifecycle.WaitError
	if !errors.As(planErr, &we) {
		t.Fatalf("expected a lifecycle.WaitError so the engine requeues, got %T: %v", planErr, planErr)
	}
	if !strings.Contains(planErr.Error(), "immutable") {
		t.Errorf("error = %q, want it to explain targetImage immutability", planErr.Error())
	}
	if failed != 1 {
		t.Errorf("failureCallback calls = %d, want 1 (this is a terminal error, not a park)", failed)
	}
	if progressed != 0 {
		t.Errorf("progressCallback calls = %d, want 0 for a terminal error", progressed)
	}
	if len(op.results.Nodes) != 2 {
		t.Errorf("results.Nodes has %d entries, want 2 (prior campaign history must survive)", len(op.results.Nodes))
	}
	if op.results.Nodes[1].PreviousImage != "registry/ssdproxy:v0" {
		t.Errorf("Nodes[1].PreviousImage = %q, want preserved %q", op.results.Nodes[1].PreviousImage, "registry/ssdproxy:v0")
	}
}

// TestRefuseIfAnotherCampaignRunning covers cross-campaign exclusion: it is a symmetric "both
// refuse" policy, not a winner-election total order. Two non-terminal campaigns must both
// see each other as a blocker; Done/Failed campaigns, a different action, and self must never count.
func TestRefuseIfAnotherCampaignRunning(t *testing.T) {
	self := &weka.WekaManualOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "self", Namespace: "ns", UID: "self-uid"},
		Spec:       weka.WekaManualOperationSpec{Action: weka.WekaManualOperationActionRotateSsdProxy},
		Status:     weka.WekaManualOperationStatus{Status: "Running"},
	}

	newOp := func(objs ...client.Object) *RotateSsdProxyOperation {
		allObjs := append([]client.Object{self}, objs...)
		fakeClient := newFakeClient(t, allObjs...)
		return &RotateSsdProxyOperation{
			mgr:      &rotateSsdProxyTestManager{reader: fakeClient},
			ownerRef: self,
		}
	}

	t.Run("two non-terminal campaigns both refuse, each naming the other", func(t *testing.T) {
		other := &weka.WekaManualOperation{
			ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "ns", UID: "other-uid"},
			Spec:       weka.WekaManualOperationSpec{Action: weka.WekaManualOperationActionRotateSsdProxy},
			Status:     weka.WekaManualOperationStatus{Status: "Running"},
		}

		selfSideErr := newOp(other).refuseIfAnotherCampaignRunning(context.Background())
		if selfSideErr == nil || !strings.Contains(selfSideErr.Error(), "other") {
			t.Errorf("self-side error = %v, want it to name %q", selfSideErr, "other")
		}

		// Symmetric: run the same check from "other"'s point of view, naming "self".
		otherOp := &RotateSsdProxyOperation{
			mgr:      &rotateSsdProxyTestManager{reader: newFakeClient(t, self, other)},
			ownerRef: other,
		}
		otherSideErr := otherOp.refuseIfAnotherCampaignRunning(context.Background())
		if otherSideErr == nil || !strings.Contains(otherSideErr.Error(), "self") {
			t.Errorf("other-side error = %v, want it to name %q", otherSideErr, "self")
		}
	})

	t.Run("a Done other is ignored", func(t *testing.T) {
		other := &weka.WekaManualOperation{
			ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "ns", UID: "other-uid"},
			Spec:       weka.WekaManualOperationSpec{Action: weka.WekaManualOperationActionRotateSsdProxy},
			Status:     weka.WekaManualOperationStatus{Status: "Done"},
		}
		if err := newOp(other).refuseIfAnotherCampaignRunning(context.Background()); err != nil {
			t.Errorf("expected no refusal against a Done campaign, got: %v", err)
		}
	})

	t.Run("a Failed other is ignored", func(t *testing.T) {
		other := &weka.WekaManualOperation{
			ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "ns", UID: "other-uid"},
			Spec:       weka.WekaManualOperationSpec{Action: weka.WekaManualOperationActionRotateSsdProxy},
			Status:     weka.WekaManualOperationStatus{Status: "Failed"},
		}
		if err := newOp(other).refuseIfAnotherCampaignRunning(context.Background()); err != nil {
			t.Errorf("expected no refusal against a Failed campaign, got: %v", err)
		}
	})

	t.Run("a deleted other is ignored even while non-terminal", func(t *testing.T) {
		// A campaign mid-deletion (e.g. Status="Running" but DeletionTimestamp set, the window between
		// `kubectl delete` and the finalizer/DeleteSelf step actually removing it) must not be treated
		// as a live competitor -- it is on its way out and refusing against it would wedge a new
		// campaign for no reason.
		now := metav1.Now()
		other := &weka.WekaManualOperation{
			ObjectMeta: metav1.ObjectMeta{
				Name: "other", Namespace: "ns", UID: "other-uid",
				DeletionTimestamp: &now,
				Finalizers:        []string{"keep-for-test"}, // fake client rejects delete-add without one
			},
			Spec:   weka.WekaManualOperationSpec{Action: weka.WekaManualOperationActionRotateSsdProxy},
			Status: weka.WekaManualOperationStatus{Status: "Running"},
		}
		if err := newOp(other).refuseIfAnotherCampaignRunning(context.Background()); err != nil {
			t.Errorf("expected no refusal against a deleted campaign, got: %v", err)
		}
	})

	t.Run("an other with empty status still blocks", func(t *testing.T) {
		// A campaign created moments ago, before its very first status write -- exactly the window
		// this check exists to cover: "" is a contender, not an ignorable phase.
		other := &weka.WekaManualOperation{
			ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "ns", UID: "other-uid"},
			Spec:       weka.WekaManualOperationSpec{Action: weka.WekaManualOperationActionRotateSsdProxy},
		}
		if err := newOp(other).refuseIfAnotherCampaignRunning(context.Background()); err == nil {
			t.Error("expected a refusal against an other with empty status")
		}
	})

	t.Run("a different action is ignored", func(t *testing.T) {
		other := &weka.WekaManualOperation{
			ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "ns", UID: "other-uid"},
			Spec:       weka.WekaManualOperationSpec{Action: weka.WekaManualOperationActionCleanStaleVirtualDrives},
			Status:     weka.WekaManualOperationStatus{Status: "Running"},
		}
		if err := newOp(other).refuseIfAnotherCampaignRunning(context.Background()); err != nil {
			t.Errorf("expected no refusal against a different action, got: %v", err)
		}
	})

	t.Run("self is excluded by UID", func(t *testing.T) {
		if err := newOp().refuseIfAnotherCampaignRunning(context.Background()); err != nil {
			t.Errorf("expected no refusal when self is the only listed campaign, got: %v", err)
		}
	})
}

// An EMPTY selector matching nothing stays a legitimate no-op (a fleet with no drive sharing): Plan
// must complete normally rather than parking.
func TestPlanDoesNotParkOnEmptyNodeSelectorWithNoProxies(t *testing.T) {
	withOperatorNamespace(t, "test-ns")

	op := &RotateSsdProxyOperation{
		mgr:      newPlanTestManager(t),
		ownerRef: &weka.WekaManualOperation{},
		payload: &weka.RotateSsdProxyPayload{
			TargetImage: "registry/ssdproxy:v2",
			// NodeSelector deliberately empty/nil.
		},
		kubeService:      &fakeSsdProxyKubeService{},
		progressCallback: func(context.Context) error { return nil },
	}

	if err := op.Plan(context.Background()); err != nil {
		t.Fatalf("expected Plan to succeed on an empty selector matching no proxies, got: %v", err)
	}
	if op.results.Total != 0 {
		t.Errorf("results.Total = %d, want 0", op.results.Total)
	}
	if op.results.Err != "" {
		t.Errorf("results.Err = %q, want empty", op.results.Err)
	}
}

// TestPlanClearsBlockedSinceOnSuccess covers BlockedSince cleanup: a campaign-scope block that
// resolves (here, a prior nodeSelector-matched-nothing park that a fresh Plan run no longer hits)
// must not leave a stale BlockedSince behind — otherwise a later, unrelated block would inherit an
// origin timestamp that has nothing to do with it, corrupting the campaign-scoped warn signal's
// elapsed-time math.
func TestPlanClearsBlockedSinceOnSuccess(t *testing.T) {
	withOperatorNamespace(t, "test-ns")

	blockedSince := metav1.NewTime(time.Now().Add(-time.Hour))
	previous := RotateSsdProxyResult{
		TargetImage:  "registry/ssdproxy:v2",
		Err:          "nodeSelector matched no nodes with an ssdproxy; check the node labels",
		BlockedSince: &blockedSince,
	}
	previousJSON, err := json.Marshal(previous)
	if err != nil {
		t.Fatalf("marshal previous result: %v", err)
	}

	op := &RotateSsdProxyOperation{
		mgr:      newPlanTestManager(t),
		ownerRef: &weka.WekaManualOperation{Status: weka.WekaManualOperationStatus{Status: "Running", Result: string(previousJSON)}},
		payload: &weka.RotateSsdProxyPayload{
			TargetImage: "registry/ssdproxy:v2",
			// NodeSelector deliberately empty/nil, so this Plan call takes the success path.
		},
		kubeService:      &fakeSsdProxyKubeService{},
		progressCallback: func(context.Context) error { return nil },
	}

	if err := op.Plan(context.Background()); err != nil {
		t.Fatalf("expected Plan to succeed, got: %v", err)
	}
	if op.results.BlockedSince != nil {
		t.Errorf("results.BlockedSince = %v, want nil after a successful Plan", op.results.BlockedSince)
	}
}

// TestAdvancePendingParksOnLiveProxyFetchError covers the first parkOnErr branch in advancePending:
// a failed re-read of the live proxy (needed for a current resourceVersion before patching) must
// park the node with a reason naming the fetch, not reach the gate at all.
func TestAdvancePendingParksOnLiveProxyFetchError(t *testing.T) {
	withOperatorNamespace(t, "test-ns")

	var persisted int
	op := &RotateSsdProxyOperation{
		// No proxy-a WekaContainer in this client at all, so liveProxy's Get fails.
		client:           newFakeClient(t),
		ownerRef:         &weka.WekaManualOperation{},
		progressCallback: func(context.Context) error { persisted++; return nil },
		results: RotateSsdProxyResult{
			Nodes: []RotateSsdProxyNodeState{{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhasePending}},
		},
	}

	err := op.advancePending(context.Background(), 0)
	var we *lifecycle.WaitError
	if !errors.As(err, &we) {
		t.Fatalf("expected a lifecycle.WaitError (park), got %T: %v", err, err)
	}
	if !strings.Contains(op.results.Nodes[0].Reason, "get proxy container") {
		t.Errorf("Nodes[0].Reason = %q, want it to name the fetch failure", op.results.Nodes[0].Reason)
	}
	if op.results.Nodes[0].Phase != RotateSsdProxyPhasePending {
		t.Errorf("Nodes[0].Phase = %q, want unchanged %q (never reached the gate)", op.results.Nodes[0].Phase, RotateSsdProxyPhasePending)
	}
	if persisted != 1 {
		t.Errorf("progressCallback calls = %d, want 1", persisted)
	}
}

// TestAdvancePendingParksOnGateError covers advancePending's second parkOnErr branch: the fetch
// succeeds but the gate itself errors (injected via fakeGate). The node must park naming the gate,
// and -- this is the persist-before-patch ordering guarantee from the other direction -- since the gate never returned
// "allowed", patchProxyImage must never be reached and the node must stay exactly where advancePending
// found it, not fall through to Phase=InFlight.
func TestAdvancePendingParksOnGateError(t *testing.T) {
	withOperatorNamespace(t, "test-ns")

	proxy := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "proxy-a", Namespace: "test-ns"},
		Spec:       weka.WekaContainerSpec{Image: "registry/ssdproxy:v1"},
	}
	fakeClient := newFakeClient(t, proxy)

	var persisted int
	op := &RotateSsdProxyOperation{
		gate:             fakeGate(nil, errors.New("boom")),
		client:           fakeClient,
		ownerRef:         &weka.WekaManualOperation{},
		progressCallback: func(context.Context) error { persisted++; return nil },
		results: RotateSsdProxyResult{
			TargetImage: "registry/ssdproxy:v2",
			Nodes:       []RotateSsdProxyNodeState{{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhasePending}},
		},
	}

	err := op.advancePending(context.Background(), 0)
	var we *lifecycle.WaitError
	if !errors.As(err, &we) {
		t.Fatalf("expected a lifecycle.WaitError (park), got %T: %v", err, err)
	}
	if !strings.Contains(op.results.Nodes[0].Reason, "evaluate disruption gate") {
		t.Errorf("Nodes[0].Reason = %q, want it to name the gate failure", op.results.Nodes[0].Reason)
	}
	if op.results.Nodes[0].Phase != RotateSsdProxyPhasePending {
		t.Errorf("Nodes[0].Phase = %q, want unchanged %q (a gate error must never advance to InFlight)", op.results.Nodes[0].Phase, RotateSsdProxyPhasePending)
	}
	if op.results.Nodes[0].PreviousImage != "" {
		t.Errorf("Nodes[0].PreviousImage = %q, want empty (never reached the intent-before-action persist)", op.results.Nodes[0].PreviousImage)
	}
	// The live proxy itself must be untouched -- a gate error must never reach patchProxyImage.
	live := &weka.WekaContainer{}
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: "proxy-a", Namespace: "test-ns"}, live); err != nil {
		t.Fatalf("get live proxy: %v", err)
	}
	if live.Spec.Image != "registry/ssdproxy:v1" {
		t.Errorf("live proxy image = %q, want untouched %q", live.Spec.Image, "registry/ssdproxy:v1")
	}
	if persisted != 1 {
		t.Errorf("progressCallback calls = %d, want 1", persisted)
	}
}

// TestAdvanceInFlightSkipsRecoveryWhenAlreadyOnTarget covers the guard at the top of
// advanceInFlight's recovery branch: a proxy whose image already equals the target (the normal,
// already-patched case) must go straight to verifyNodeComplete and must never call the gate at all --
// not "call it and get lucky", never call it. Demonstrated here by injecting a gate (via fakeGate)
// that would deterministically error if reached: if the guard were removed or inverted, this test
// would fail on the gate error instead of reaching verifyNodeComplete's own (equally real)
// liveProxyAndPod-adjacent park.
func TestAdvanceInFlightSkipsRecoveryWhenAlreadyOnTarget(t *testing.T) {
	withOperatorNamespace(t, "test-ns")

	proxy := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "proxy-a", Namespace: "test-ns"},
		Spec:       weka.WekaContainerSpec{Image: "registry/ssdproxy:v2"}, // already on target
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "proxy-a", Namespace: "test-ns"}}
	fakeClient := newFakeClient(t, proxy, pod)

	var persisted int
	op := &RotateSsdProxyOperation{
		gate:             fakeGate(nil, errors.New("gate must not be called when already on target")),
		client:           fakeClient,
		ownerRef:         &weka.WekaManualOperation{},
		progressCallback: func(context.Context) error { persisted++; return nil },
		results: RotateSsdProxyResult{
			TargetImage: "registry/ssdproxy:v2",
			Nodes:       []RotateSsdProxyNodeState{{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhaseInFlight}},
		},
	}

	err := op.advanceInFlight(context.Background(), 0)
	var we *lifecycle.WaitError
	if !errors.As(err, &we) {
		t.Fatalf("expected a lifecycle.WaitError, got %T: %v", err, err)
	}
	// verifyNodeComplete parks (pod/proxy status fields are zero-valued, so podAndProxyReady is
	// false) rather than erroring -- proof the recovery branch's gate was never reached, since a
	// reached-and-failed gate would have produced a "re-apply target image" reason instead.
	if strings.Contains(op.results.Nodes[0].Reason, "re-apply target image") {
		t.Errorf("Nodes[0].Reason = %q, recovery branch's gate must not have been reached", op.results.Nodes[0].Reason)
	}
	if persisted != 1 {
		t.Errorf("progressCallback calls = %d, want 1", persisted)
	}
}

// TestAdvanceInFlightParksWhenPodIsGoneButProxyIsOnTarget covers a Pod that vanishes
// mid-rotation (e.g. the ssdproxy DaemonSet's own restart of it) is a normal, expected rotation
// state, not an infra error worth failing the campaign over. liveProxyAndPod must swallow the
// Pod's NotFound into (proxy, nil, nil), and advanceInFlight must park describing it as waiting for
// recreation -- not as a generic "failed to get" error.
func TestAdvanceInFlightParksWhenPodIsGoneButProxyIsOnTarget(t *testing.T) {
	withOperatorNamespace(t, "test-ns")

	proxy := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "proxy-a", Namespace: "test-ns"},
		Spec:       weka.WekaContainerSpec{Image: "registry/ssdproxy:v2"}, // already on target
	}
	// Deliberately no Pod object -- the fake client's Get will return a real NotFound.
	fakeClient := newFakeClient(t, proxy)

	op := &RotateSsdProxyOperation{
		gate:             fakeGate(nil, errors.New("gate must not be called while the pod is gone")),
		client:           fakeClient,
		ownerRef:         &weka.WekaManualOperation{},
		progressCallback: func(context.Context) error { return nil },
		results: RotateSsdProxyResult{
			TargetImage: "registry/ssdproxy:v2",
			Nodes:       []RotateSsdProxyNodeState{{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhaseInFlight}},
		},
	}

	err := op.advanceInFlight(context.Background(), 0)
	var we *lifecycle.WaitError
	if !errors.As(err, &we) {
		t.Fatalf("expected a lifecycle.WaitError (a park, not an infra failure), got %T: %v", err, err)
	}
	if got := op.results.Nodes[0].Reason; got != "pod deleted, waiting for recreation" {
		t.Errorf("Nodes[0].Reason = %q, want %q", got, "pod deleted, waiting for recreation")
	}
	if op.results.Nodes[0].Phase != RotateSsdProxyPhaseInFlight {
		t.Errorf("Nodes[0].Phase = %q, want still InFlight (a missing pod is not a failure)", op.results.Nodes[0].Phase)
	}
}

// TestAdvanceInFlightRecoveryBranchParksOnGateError covers the InFlight-but-unpatched recovery state
// advancePending's persist-before-patch ordering makes reachable: proxy.Spec.Image !=
// TargetImage on an InFlight node. applyTargetImage's gate call errors (injected via fakeGate, same
// mechanism as TestAdvancePendingParksOnGateError) and the node must park naming the re-apply step,
// not fall through to Done.
func TestAdvanceInFlightRecoveryBranchParksOnGateError(t *testing.T) {
	withOperatorNamespace(t, "test-ns")

	proxy := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "proxy-a", Namespace: "test-ns"},
		Spec:       weka.WekaContainerSpec{Image: "registry/ssdproxy:v1"}, // not yet re-patched
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "proxy-a", Namespace: "test-ns"}}
	fakeClient := newFakeClient(t, proxy, pod)

	var persisted int
	op := &RotateSsdProxyOperation{
		gate:             fakeGate(nil, errors.New("boom")),
		client:           fakeClient,
		ownerRef:         &weka.WekaManualOperation{},
		progressCallback: func(context.Context) error { persisted++; return nil },
		results: RotateSsdProxyResult{
			TargetImage: "registry/ssdproxy:v2",
			Nodes:       []RotateSsdProxyNodeState{{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhaseInFlight, PreviousImage: "registry/ssdproxy:v0"}},
		},
	}

	err := op.advanceInFlight(context.Background(), 0)
	var we *lifecycle.WaitError
	if !errors.As(err, &we) {
		t.Fatalf("expected a lifecycle.WaitError (park), got %T: %v", err, err)
	}
	if !strings.Contains(op.results.Nodes[0].Reason, "re-apply target image") {
		t.Errorf("Nodes[0].Reason = %q, want it to name the re-apply failure", op.results.Nodes[0].Reason)
	}
	if op.results.Nodes[0].Phase != RotateSsdProxyPhaseInFlight {
		t.Errorf("Nodes[0].Phase = %q, want unchanged %q", op.results.Nodes[0].Phase, RotateSsdProxyPhaseInFlight)
	}
	// The live proxy must still be unpatched -- a gate error must never reach patchProxyImage.
	live := &weka.WekaContainer{}
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: "proxy-a", Namespace: "test-ns"}, live); err != nil {
		t.Fatalf("get live proxy: %v", err)
	}
	if live.Spec.Image != "registry/ssdproxy:v1" {
		t.Errorf("live proxy image = %q, want untouched %q", live.Spec.Image, "registry/ssdproxy:v1")
	}
	if persisted != 1 {
		t.Errorf("progressCallback calls = %d, want 1", persisted)
	}
}

// orderTrackingClient wraps a client.Client, appending "patch" to a shared log on every Patch call.
// Paired with a progressCallback that appends "persist" to the same log, it lets a test assert that
// two operations happened in a specific order relative to each other, not merely that both happened —
// which is the actual guarantee here (see advancePending's "intent before action" comment).
type orderTrackingClient struct {
	client.Client
	log *[]string
}

func (c *orderTrackingClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	*c.log = append(*c.log, "patch")
	return c.Client.Patch(ctx, obj, patch, opts...)
}

// TestAdvancePendingPersistsBeforePatchingWhenGateAllows is the central ordering guarantee,
// asserted directly: when the gate allows, advancePending must call persist (progressCallback)
// BEFORE patchProxyImage (client.Patch) -- not just call both. A crash between the two must find
// Phase=InFlight already durable, which is only true if persist ran first. Counting calls alone
// cannot catch a regression that swaps the two lines; only call order can.
func TestAdvancePendingPersistsBeforePatchingWhenGateAllows(t *testing.T) {
	withOperatorNamespace(t, "test-ns")

	proxy := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "proxy-a", Namespace: "test-ns"},
		Spec:       weka.WekaContainerSpec{Image: "registry/ssdproxy:v1"},
	}
	fakeClient := newFakeClient(t, proxy)

	var log []string
	op := &RotateSsdProxyOperation{
		gate:             fakeGate(nil, nil), // AllAllowed(nil) == allowed
		client:           &orderTrackingClient{Client: fakeClient, log: &log},
		ownerRef:         &weka.WekaManualOperation{},
		progressCallback: func(context.Context) error { log = append(log, "persist"); return nil },
		results: RotateSsdProxyResult{
			TargetImage: "registry/ssdproxy:v2",
			Nodes:       []RotateSsdProxyNodeState{{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhasePending}},
		},
	}

	err := op.advancePending(context.Background(), 0)
	var we *lifecycle.WaitError
	if !errors.As(err, &we) {
		t.Fatalf("expected a lifecycle.WaitError, got %T: %v", err, err)
	}

	want := []string{"persist", "patch"}
	if len(log) != len(want) {
		t.Fatalf("call log = %v, want %v", log, want)
	}
	for i := range want {
		if log[i] != want[i] {
			t.Fatalf("call log = %v, want %v (persist must precede patch)", log, want)
		}
	}

	if op.results.Nodes[0].Phase != RotateSsdProxyPhaseInFlight {
		t.Errorf("Nodes[0].Phase = %q, want %q", op.results.Nodes[0].Phase, RotateSsdProxyPhaseInFlight)
	}
	if op.results.Nodes[0].PreviousImage != "registry/ssdproxy:v1" {
		t.Errorf("Nodes[0].PreviousImage = %q, want %q", op.results.Nodes[0].PreviousImage, "registry/ssdproxy:v1")
	}
}

// patchFailingClient wraps a client.Client, forcing every Patch call to fail -- unlike a Get
// failure (parked before Phase ever becomes InFlight), this simulates the patch itself failing
// after intent (Phase=InFlight) has already been persisted.
type patchFailingClient struct {
	client.Client
}

func (c *patchFailingClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return errors.New("simulated patch failure")
}

// TestAdvancePendingLeavesNodeInFlightWhenPatchFailsAfterGateAllows covers the failure mode the persist-before-patch ordering
// exists for: the gate allows, Phase=InFlight and PreviousImage are persisted, and then the patch
// itself fails. The node must be left InFlight with PreviousImage set -- NOT rolled back to
// Pending -- so advanceInFlight's recovery branch can find and re-patch it on the next reconcile
// (see advancePending's "intent before action" comment).
func TestAdvancePendingLeavesNodeInFlightWhenPatchFailsAfterGateAllows(t *testing.T) {
	withOperatorNamespace(t, "test-ns")

	proxy := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "proxy-a", Namespace: "test-ns"},
		Spec:       weka.WekaContainerSpec{Image: "registry/ssdproxy:v1"},
	}
	fakeClient := newFakeClient(t, proxy)

	var persisted int
	op := &RotateSsdProxyOperation{
		gate:             fakeGate(nil, nil), // allowed
		client:           &patchFailingClient{Client: fakeClient},
		ownerRef:         &weka.WekaManualOperation{},
		progressCallback: func(context.Context) error { persisted++; return nil },
		results: RotateSsdProxyResult{
			TargetImage: "registry/ssdproxy:v2",
			Nodes:       []RotateSsdProxyNodeState{{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhasePending}},
		},
	}

	err := op.advancePending(context.Background(), 0)
	var we *lifecycle.WaitError
	if !errors.As(err, &we) {
		t.Fatalf("expected a lifecycle.WaitError (park), got %T: %v", err, err)
	}
	if !strings.Contains(op.results.Nodes[0].Reason, "patch proxy image") {
		t.Errorf("Nodes[0].Reason = %q, want it to name the patch failure", op.results.Nodes[0].Reason)
	}
	if op.results.Nodes[0].Phase != RotateSsdProxyPhaseInFlight {
		t.Errorf("Nodes[0].Phase = %q, want %q (already persisted before the patch failed, must not roll back)", op.results.Nodes[0].Phase, RotateSsdProxyPhaseInFlight)
	}
	if op.results.Nodes[0].PreviousImage != "registry/ssdproxy:v1" {
		t.Errorf("Nodes[0].PreviousImage = %q, want %q", op.results.Nodes[0].PreviousImage, "registry/ssdproxy:v1")
	}
	if persisted != 2 {
		// Once for the intent-before-action persist, once more from parkNode's own persist.
		t.Errorf("progressCallback calls = %d, want 2", persisted)
	}
}

// TestAdvanceInFlightRecoveryRepatchesWhenGateAllows covers the recovery branch's success path:
// proxy.Spec.Image != TargetImage on an InFlight node (advancePending persisted intent but never
// reached, or never completed, the patch) and the gate allows on re-check. applyTargetImage must
// actually re-patch the live proxy to TargetImage, not just report "not blocked".
//
// Doubles as the pin for evaluateGate's allow path clearing node.Reason alongside o.results.Blocked:
// this branch persists immediately after applyTargetImage returns and gets no other chance to
// refresh Reason before that write (advancePending clears it itself), so last cycle's block reason
// would land in status.result on a node that just made progress. Both are seeded stale below.
func TestAdvanceInFlightRecoveryRepatchesWhenGateAllows(t *testing.T) {
	withOperatorNamespace(t, "test-ns")

	proxy := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "proxy-a", Namespace: "test-ns"},
		Spec:       weka.WekaContainerSpec{Image: "registry/ssdproxy:v1"}, // not yet re-patched
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "proxy-a", Namespace: "test-ns"}}
	fakeClient := newFakeClient(t, proxy, pod)

	var persisted int
	op := &RotateSsdProxyOperation{
		gate:             fakeGate(nil, nil), // allowed
		client:           fakeClient,
		ownerRef:         &weka.WekaManualOperation{},
		progressCallback: func(context.Context) error { persisted++; return nil },
		results: RotateSsdProxyResult{
			TargetImage: "registry/ssdproxy:v2",
			// Stale block state from the previous cycle, as a crash between gate and patch leaves it.
			Blocked: []ClusterVerdict{{Namespace: "ns", Name: "cluster-a", Allowed: false, Reason: "rebuild is moving data"}},
			Nodes: []RotateSsdProxyNodeState{{
				Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhaseInFlight,
				PreviousImage: "registry/ssdproxy:v0", Reason: "pod deleted, waiting for recreation",
			}},
		},
	}

	err := op.advanceInFlight(context.Background(), 0)
	var we *lifecycle.WaitError
	if !errors.As(err, &we) {
		t.Fatalf("expected a lifecycle.WaitError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "re-applied target image") {
		t.Errorf("err = %q, want it to name the re-apply wait", err.Error())
	}
	if op.results.Nodes[0].Reason != "" {
		t.Errorf("Nodes[0].Reason = %q, want it cleared once the gate allowed and the re-patch landed", op.results.Nodes[0].Reason)
	}
	if op.results.Blocked != nil {
		t.Errorf("Blocked = %v, want nil on an allowed verdict", op.results.Blocked)
	}
	live := &weka.WekaContainer{}
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: "proxy-a", Namespace: "test-ns"}, live); err != nil {
		t.Fatalf("get live proxy: %v", err)
	}
	if live.Spec.Image != "registry/ssdproxy:v2" {
		t.Errorf("live proxy image = %q, want re-patched to target %q", live.Spec.Image, "registry/ssdproxy:v2")
	}
	if op.results.Nodes[0].Phase != RotateSsdProxyPhaseInFlight {
		t.Errorf("Nodes[0].Phase = %q, want unchanged %q", op.results.Nodes[0].Phase, RotateSsdProxyPhaseInFlight)
	}
	if persisted != 1 {
		t.Errorf("progressCallback calls = %d, want 1", persisted)
	}
}

// TestAdvanceInFlightRecoveryParksWhenGateBlocks covers the recovery branch's block path,
// distinct from TestAdvanceInFlightRecoveryBranchParksOnGateError (which covers a gate error): here
// the gate runs cleanly and simply disallows (AllAllowed == false, err == nil). No patch must be
// attempted, and the node parks -- staying InFlight, not advancing to Done and not rolling back to
// Pending -- carrying the gate's verdicts/reason for parkNode to surface.
func TestAdvanceInFlightRecoveryParksWhenGateBlocks(t *testing.T) {
	withOperatorNamespace(t, "test-ns")

	proxy := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "proxy-a", Namespace: "test-ns"},
		Spec:       weka.WekaContainerSpec{Image: "registry/ssdproxy:v1"}, // not yet re-patched
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "proxy-a", Namespace: "test-ns"}}
	fakeClient := newFakeClient(t, proxy, pod)

	blockingVerdicts := []ClusterVerdict{{Namespace: "ns", Name: "cluster-a", Allowed: false, Reason: "rebuild in progress"}}

	var persisted int
	op := &RotateSsdProxyOperation{
		gate:             fakeGate(blockingVerdicts, nil),
		client:           fakeClient,
		ownerRef:         &weka.WekaManualOperation{},
		progressCallback: func(context.Context) error { persisted++; return nil },
		results: RotateSsdProxyResult{
			TargetImage: "registry/ssdproxy:v2",
			Nodes:       []RotateSsdProxyNodeState{{Node: "node-a", ProxyName: "proxy-a", Phase: RotateSsdProxyPhaseInFlight, PreviousImage: "registry/ssdproxy:v0"}},
		},
	}

	err := op.advanceInFlight(context.Background(), 0)
	var we *lifecycle.WaitError
	if !errors.As(err, &we) {
		t.Fatalf("expected a lifecycle.WaitError (park), got %T: %v", err, err)
	}
	if op.results.Nodes[0].Reason != "rebuild in progress" {
		t.Errorf("Nodes[0].Reason = %q, want the gate's blocking reason %q", op.results.Nodes[0].Reason, "rebuild in progress")
	}
	if op.results.Nodes[0].Phase != RotateSsdProxyPhaseInFlight {
		t.Errorf("Nodes[0].Phase = %q, want unchanged %q", op.results.Nodes[0].Phase, RotateSsdProxyPhaseInFlight)
	}
	if len(op.results.Blocked) != 1 || op.results.Blocked[0].Reason != "rebuild in progress" {
		t.Errorf("results.Blocked = %+v, want the blocking verdict recorded", op.results.Blocked)
	}
	// The live proxy must be untouched -- a blocked gate must never reach patchProxyImage.
	live := &weka.WekaContainer{}
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Name: "proxy-a", Namespace: "test-ns"}, live); err != nil {
		t.Fatalf("get live proxy: %v", err)
	}
	if live.Spec.Image != "registry/ssdproxy:v1" {
		t.Errorf("live proxy image = %q, want untouched %q", live.Spec.Image, "registry/ssdproxy:v1")
	}
	if persisted != 1 {
		t.Errorf("progressCallback calls = %d, want 1", persisted)
	}
}
