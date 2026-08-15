package wekacluster

import (
	"context"
	"strings"
	"testing"

	"github.com/weka/go-steps-engine/lifecycle"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func pausableContainer(name, mode string) *weka.WekaContainer {
	c := &weka.WekaContainer{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	c.Spec.Mode = mode
	c.Spec.State = weka.ContainerStateActive
	return c
}

// findStep returns the first step in the reconcile loop whose name starts with prefix.
// Step names come from lifecycle.SimpleStep.GetName(), which falls back to the name of the
// function the step runs (e.g. "HandleManualPause-fm") when no explicit Name is set.
func findStep(steps []lifecycle.Step, prefix string) lifecycle.Step {
	for _, s := range steps {
		if strings.HasPrefix(s.GetName(), prefix) {
			return s
		}
		if s.HasNestedSteps() {
			if nested := findStep(s.GetNestedSteps(), prefix); nested != nil {
				return nested
			}
		}
	}
	return nil
}

// stepPredicatesPass reports whether every predicate on the step is currently true, i.e.
// whether the step engine would run it on this reconcile.
func stepPredicatesPass(s lifecycle.Step) bool {
	for _, p := range s.GetPredicates() {
		if !p() {
			return false
		}
	}
	return true
}

// TestManualPauseStepIsWiredIntoReconcileLoop is the regression guard for OP-375.
//
// The original breakage was not in HandleManualPause's body - that function was correct.
// It was that the step referencing it was dropped from GetAllSteps() by a dead-code sweep
// (7c814f49), which made spec.overrides.paused a silent no-op. A test that calls
// HandleManualPause directly stays green through exactly that failure, so this one asserts
// the wiring instead.
func TestManualPauseStepIsWiredIntoReconcileLoop(t *testing.T) {
	loop := &wekaClusterReconcilerLoop{cluster: &weka.WekaCluster{}}

	if findStep(loop.GetAllSteps(), "HandleManualPause") == nil {
		t.Fatal("HandleManualPause is not wired into GetAllSteps - spec.overrides.paused is a no-op (OP-375)")
	}
}

// TestManualPauseAndDeletionPathsAreMutuallyExclusive pins the pause/deletion matrix
// documented in doc/operator/operations/pause.md: paused=true suspends reconciliation
// unless the cluster is actively being deleted, and cancelDeletion rescues it back into
// the pause path. Exactly one of the two paths may be eligible on any given reconcile.
func TestManualPauseAndDeletionPathsAreMutuallyExclusive(t *testing.T) {
	truePtr, falsePtr := true, false

	cases := []struct {
		name              string
		paused            *bool
		markedForDeletion bool
		cancelDeletion    bool
		wantPause         bool
		wantDeletion      bool
	}{
		{name: "paused, no deletion", paused: &truePtr, wantPause: true},
		{name: "paused, marked for deletion", paused: &truePtr, markedForDeletion: true, wantDeletion: true},
		{name: "paused, deletion cancelled", paused: &truePtr, markedForDeletion: true, cancelDeletion: true, wantPause: true},
		{name: "explicitly unpaused, no deletion", paused: &falsePtr},
		{name: "paused not set, no deletion", paused: nil},
		{name: "paused not set, marked for deletion", paused: nil, markedForDeletion: true, wantDeletion: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cluster := &weka.WekaCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default"},
				Spec: weka.WekaClusterSpec{
					Overrides: &weka.WekaClusterSpecOverrides{
						Paused:         c.paused,
						CancelDeletion: c.cancelDeletion,
					},
				},
			}
			if c.markedForDeletion {
				now := metav1.Now()
				cluster.DeletionTimestamp = &now
			}

			// The steps capture cluster state as method values at construction time, so the
			// step list has to be rebuilt per scenario rather than mutating the cluster.
			steps := (&wekaClusterReconcilerLoop{cluster: cluster}).GetAllSteps()

			pauseStep := findStep(steps, "HandleManualPause")
			if pauseStep == nil {
				t.Fatal("HandleManualPause is not wired into GetAllSteps (OP-375)")
			}
			deletionStep := findStep(steps, "DeletionPath")
			if deletionStep == nil {
				t.Fatal("DeletionPath group is not wired into GetAllSteps")
			}

			if got := stepPredicatesPass(pauseStep); got != c.wantPause {
				t.Errorf("manual pause step eligible = %v, want %v", got, c.wantPause)
			}
			if got := stepPredicatesPass(deletionStep); got != c.wantDeletion {
				t.Errorf("deletion path eligible = %v, want %v", got, c.wantDeletion)
			}
			if c.wantPause && c.wantDeletion {
				t.Fatal("test case is malformed: pause and deletion paths must be mutually exclusive")
			}
		})
	}
}

// TestHandleManualPause_PausesContainersAndSetsStatus asserts that setting overrides.paused=true
// drives the cluster to status Paused and drains every container to spec.state=paused.
//
// The drain is requeue-driven: ensureContainersPaused patches a container's spec and then returns
// an error until that container reports Status.Status=Paused, so a real pause spans several
// reconciles. Phase 1 covers the first pass (status set, protocol containers drained first),
// phase 2 covers the pass where the containers have caught up.
func TestHandleManualPause_PausesContainersAndSetsStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add weka scheme: %v", err)
	}

	paused := true
	cluster := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default"},
		Spec: weka.WekaClusterSpec{
			Overrides: &weka.WekaClusterSpecOverrides{Paused: &paused},
		},
	}
	cluster.Status.Status = weka.WekaClusterStatusReady

	// One container per protocol mode in protocolDrainOrder, so dropping a mode from that
	// list fails this test instead of silently skipping those containers.
	s3Container := pausableContainer("c-s3", weka.WekaContainerModeS3)
	nfsContainer := pausableContainer("c-nfs", weka.WekaContainerModeNfs)
	smbwContainer := pausableContainer("c-smbw", weka.WekaContainerModeSmbw)
	dataServicesContainer := pausableContainer("c-data-services", weka.WekaContainerModeDataServices)
	computeContainer := pausableContainer("c-compute", weka.WekaContainerModeCompute)
	driveContainer := pausableContainer("c-drive", weka.WekaContainerModeDrive)

	protocolContainers := []*weka.WekaContainer{s3Container, nfsContainer, smbwContainer, dataServicesContainer}
	backendContainers := []*weka.WekaContainer{computeContainer, driveContainer}
	containers := append(append([]*weka.WekaContainer{}, protocolContainers...), backendContainers...)

	objects := make([]client.Object, 0, len(containers)+1)
	objects = append(objects, cluster)
	for _, c := range containers {
		objects = append(objects, c)
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(objects...).
		Build()

	loop := &wekaClusterReconcilerLoop{
		Manager:    &fakeManager{client: fakeClient},
		cluster:    cluster,
		containers: containers,
	}

	ctx := context.Background()

	// The drain advances one mode per reconcile: ensureContainersPaused patches the containers
	// of a mode and then errors until they report Paused, so the next mode is only reached on a
	// later pass. Walking protocolDrainOrder asserts that exact sequence - a mode dropped from
	// the list would leave its container active when the stage expects it patched.
	drainStages := []struct {
		mode       string
		containers []*weka.WekaContainer
	}{
		{weka.WekaContainerModeS3, []*weka.WekaContainer{s3Container}},
		{weka.WekaContainerModeNfs, []*weka.WekaContainer{nfsContainer}},
		{weka.WekaContainerModeSmbw, []*weka.WekaContainer{smbwContainer}},
		{weka.WekaContainerModeDataServices, []*weka.WekaContainer{dataServicesContainer}},
		{"", backendContainers},
	}

	for i, stage := range drainStages {
		// The error must be the drain's own "not paused yet" and not, say, a failed status
		// update - otherwise this assertion would pass for the wrong reason.
		err := loop.HandleManualPause(ctx)
		if err == nil {
			t.Fatalf("stage %d (mode %q): expected an error while containers are still draining, got nil", i, stage.mode)
		}
		if !strings.Contains(err.Error(), "is not paused yet") {
			t.Fatalf("stage %d (mode %q): expected a drain-in-progress error, got: %v", i, stage.mode, err)
		}

		// The status flip happens before the drain, so it is visible from the very first pass.
		if cluster.Status.Status != weka.WekaClusterStatusPaused {
			t.Fatalf("stage %d (mode %q): expected cluster status %q, got %q", i, stage.mode, weka.WekaClusterStatusPaused, cluster.Status.Status)
		}

		for _, c := range stage.containers {
			if c.Spec.State != weka.ContainerStatePaused {
				t.Errorf("stage %d (mode %q): expected %s to be patched, got state %q", i, stage.mode, c.Name, c.Spec.State)
			}
		}
		for _, later := range drainStages[i+1:] {
			for _, c := range later.containers {
				if c.Spec.State != weka.ContainerStateActive {
					t.Errorf("stage %d (mode %q): expected %s (mode %q) to still be active, got state %q", i, stage.mode, c.Name, later.mode, c.Spec.State)
				}
			}
		}

		// The stage's containers catch up. This has to be persisted through the client, not just
		// set in memory - ensureContainersPaused patches each container, and the patch response
		// refreshes the in-memory object from stored state.
		for _, c := range stage.containers {
			c.Status.Status = weka.Paused
			if err = fakeClient.Status().Update(ctx, c); err != nil {
				t.Fatalf("failed to update status for %s: %v", c.Name, err)
			}
		}
	}

	// Every container now reports Paused, so the drain completes and the step succeeds.
	if err := loop.HandleManualPause(ctx); err != nil {
		t.Fatalf("expected no error once all containers report paused, got: %v", err)
	}

	for _, c := range containers {
		if c.Spec.State != weka.ContainerStatePaused {
			t.Errorf("expected %s to be paused, got state %q", c.Name, c.Spec.State)
		}
	}
	if cluster.Status.Status != weka.WekaClusterStatusPaused {
		t.Errorf("expected cluster status to remain %q, got %q", weka.WekaClusterStatusPaused, cluster.Status.Status)
	}
}
