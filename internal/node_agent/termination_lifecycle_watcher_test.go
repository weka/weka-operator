package node_agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
)

// --- fakes -------------------------------------------------------------------

type fakeStates struct {
	instanceID  string
	region      string
	identityErr error

	states       []string // sequence returned by successive TargetLifecycleState calls
	stateErr     error
	stateCalls   int
	identityCall int
}

func (f *fakeStates) InstanceIdentity(_ context.Context) (string, string, error) {
	f.identityCall++
	return f.instanceID, f.region, f.identityErr
}

func (f *fakeStates) TargetLifecycleState(_ context.Context) (string, error) {
	if f.stateErr != nil {
		return "", f.stateErr
	}
	if len(f.states) == 0 {
		return "InService", nil
	}
	idx := f.stateCalls
	if idx >= len(f.states) {
		idx = len(f.states) - 1
	}
	f.stateCalls++
	return f.states[idx], nil
}

type fakeASG struct {
	asgName     string
	describeErr error

	heartbeatCalls int
	completeCalls  []string // recorded LifecycleActionResult values
}

func (f *fakeASG) DescribeInstanceASG(_ context.Context, _ string) (string, string, error) {
	if f.describeErr != nil {
		return "", "", f.describeErr
	}
	return f.asgName, "Terminating:Wait", nil
}

func (f *fakeASG) RecordHeartbeat(_ context.Context, _, _, _ string) error {
	f.heartbeatCalls++
	return nil
}

func (f *fakeASG) CompleteAction(_ context.Context, _, _, _, result string) error {
	f.completeCalls = append(f.completeCalls, result)
	return nil
}

type fakeChecker struct {
	allowed bool
	err     error
	// onCall lets a test inject a side effect (e.g. cancel ctx) on each Allowed() invocation.
	onCall func()
}

func (f *fakeChecker) Allowed(_ context.Context) (bool, error) {
	if f.onCall != nil {
		f.onCall()
	}
	return f.allowed, f.err
}

func newTestWatcher(states lifecycleStateProvider, asg asgClient, checker deactivationChecker) *TerminationLifecycleWatcher {
	return &TerminationLifecycleWatcher{
		logger:            logr.Discard(),
		states:            states,
		asg:               asg,
		checker:           checker,
		hookName:          "weka-drive-drain",
		maxHold:           2 * time.Hour,
		pollInterval:      time.Millisecond,
		heartbeatInterval: time.Millisecond,
		now:               time.Now,
	}
}

// --- hold() tests --------------------------------------------------------------

func TestHold_PermittedCompletesImmediately(t *testing.T) {
	asg := &fakeASG{asgName: "asg-1"}
	checker := &fakeChecker{allowed: true}
	w := newTestWatcher(&fakeStates{}, asg, checker)

	if err := w.hold(context.Background(), "i-123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(asg.completeCalls) != 1 || asg.completeCalls[0] != "CONTINUE" {
		t.Fatalf("expected exactly one CompleteAction(CONTINUE), got %v", asg.completeCalls)
	}
	if asg.heartbeatCalls != 0 {
		t.Fatalf("expected no heartbeats when permitted immediately, got %d", asg.heartbeatCalls)
	}
}

func TestHold_NotPermittedRecordsHeartbeat(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	asg := &fakeASG{asgName: "asg-1"}
	checker := &fakeChecker{allowed: false, onCall: cancel} // cancel right after the first check
	w := newTestWatcher(&fakeStates{}, asg, checker)

	err := w.hold(ctx, "i-123")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if asg.heartbeatCalls != 1 {
		t.Fatalf("expected exactly one heartbeat, got %d", asg.heartbeatCalls)
	}
	if len(asg.completeCalls) != 0 {
		t.Fatalf("expected no CompleteAction while not permitted, got %v", asg.completeCalls)
	}
}

func TestHold_CheckerErrorFailsClosedAndHeartbeats(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	asg := &fakeASG{asgName: "asg-1"}
	checker := &fakeChecker{allowed: true, err: errors.New("exec failed"), onCall: cancel}
	w := newTestWatcher(&fakeStates{}, asg, checker)

	err := w.hold(ctx, "i-123")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if asg.heartbeatCalls != 1 {
		t.Fatalf("expected fail-closed heartbeat on checker error, got %d heartbeats", asg.heartbeatCalls)
	}
	if len(asg.completeCalls) != 0 {
		t.Fatalf("expected no CompleteAction on checker error, got %v", asg.completeCalls)
	}
}

func TestHold_MaxHoldExceededCompletes(t *testing.T) {
	asg := &fakeASG{asgName: "asg-1"}
	checker := &fakeChecker{allowed: false}
	w := newTestWatcher(&fakeStates{}, asg, checker)
	w.maxHold = time.Minute

	base := time.Now()
	callNum := 0
	w.now = func() time.Time {
		callNum++
		if callNum == 1 {
			return base // "start" timestamp captured at top of hold()
		}
		return base.Add(2 * time.Hour) // every subsequent check is far past max hold
	}

	if err := w.hold(context.Background(), "i-123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(asg.completeCalls) != 1 || asg.completeCalls[0] != "CONTINUE" {
		t.Fatalf("expected exactly one CompleteAction(CONTINUE) on max-hold, got %v", asg.completeCalls)
	}
}

func TestHold_DescribeInstanceASGErrorPropagates(t *testing.T) {
	asg := &fakeASG{describeErr: errors.New("no such instance")}
	w := newTestWatcher(&fakeStates{}, asg, &fakeChecker{allowed: true})

	if err := w.hold(context.Background(), "i-123"); err == nil {
		t.Fatalf("expected error when ASG cannot be resolved")
	}
}

// --- Run() tests ---------------------------------------------------------------

func TestRun_DisabledIsNoop(t *testing.T) {
	states := &fakeStates{}
	w := newTestWatcher(states, &fakeASG{}, &fakeChecker{})
	w.hookName = "" // empty hook name disables the watcher

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("expected nil error for disabled watcher, got %v", err)
	}
	if states.identityCall != 0 {
		t.Fatalf("expected IMDS not to be consulted when disabled, got %d calls", states.identityCall)
	}
}

func TestRun_IMDSUnreachableIsNoop(t *testing.T) {
	states := &fakeStates{identityErr: errors.New("IMDS unreachable")}
	asg := &fakeASG{}
	w := newTestWatcher(states, asg, &fakeChecker{})

	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("expected nil error when IMDS is unreachable, got %v", err)
	}
	if asg.heartbeatCalls != 0 || len(asg.completeCalls) != 0 {
		t.Fatalf("expected no ASG interaction when IMDS is unreachable")
	}
}

func TestRun_NotTerminatingStaysIdleNoASGCalls(t *testing.T) {
	states := &fakeStates{instanceID: "i-123", region: "us-east-1", states: []string{"InService"}}
	asg := &fakeASG{asgName: "asg-1"}
	w := newTestWatcher(states, asg, &fakeChecker{allowed: true})
	w.pollInterval = time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	if err := w.Run(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if asg.heartbeatCalls != 0 || len(asg.completeCalls) != 0 {
		t.Fatalf("expected no ASG calls while lifecycle state never indicates termination, got heartbeats=%d completes=%v", asg.heartbeatCalls, asg.completeCalls)
	}
}

func TestRun_PendingTerminationEntersHoldAndCompletes(t *testing.T) {
	states := &fakeStates{instanceID: "i-123", region: "us-east-1", states: []string{"Terminated:Wait"}}
	asg := &fakeASG{asgName: "asg-1"}
	w := newTestWatcher(states, asg, &fakeChecker{allowed: true})
	w.pollInterval = time.Millisecond
	w.heartbeatInterval = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// Run keeps cycling idle->hold indefinitely (it resumes idle detection after each release),
	// so we only assert that a release happened at least once, then stop the watcher.
	deadline := time.Now().Add(500 * time.Millisecond)
	for len(asg.completeCalls) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done

	if len(asg.completeCalls) == 0 || asg.completeCalls[0] != "CONTINUE" {
		t.Fatalf("expected at least one CompleteAction(CONTINUE), got %v", asg.completeCalls)
	}
}

// --- driveContainerPresenceChecker tests --------------------------------------

func TestDriveContainerPresenceChecker_NoDriveContainerOnNodeAllowsImmediately(t *testing.T) {
	orig := config.Config.MetricsServerEnv.NodeName
	config.Config.MetricsServerEnv.NodeName = "node-a"
	defer func() { config.Config.MetricsServerEnv.NodeName = orig }()

	scheme := watcherScheme()
	// Only a compute container exists, and it's on a different node.
	other := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "compute-1", Namespace: "ns"},
		Spec:       weka.WekaContainerSpec{Mode: weka.WekaContainerModeCompute, NodeAffinity: "node-b"},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(other).Build()

	checker := &driveContainerPresenceChecker{k8sClient: fakeClient}
	allowed, err := checker.Allowed(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatalf("expected Allowed to return true when there is no drive container on this node")
	}
}

func TestDriveContainerPresenceChecker_DriveContainerPresentHolds(t *testing.T) {
	orig := config.Config.MetricsServerEnv.NodeName
	config.Config.MetricsServerEnv.NodeName = "node-a"
	defer func() { config.Config.MetricsServerEnv.NodeName = orig }()

	scheme := watcherScheme()
	// This node's drive container CR still exists (even if mid-deletion) -> must hold.
	drive := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "drive-1", Namespace: "ns"},
		Spec:       weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive, NodeAffinity: "node-a"},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(drive).Build()

	checker := &driveContainerPresenceChecker{k8sClient: fakeClient}
	allowed, err := checker.Allowed(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Fatalf("expected Allowed to return false (hold) while this node's drive container CR still exists")
	}
}
