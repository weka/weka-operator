package operations

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/go-steps-engine/lifecycle"
)

func newKernelizeTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func newKernelizeTestOp(scheme *runtime.Scheme, existing ...client.Object) *KernelizeOperation {
	builder := fake.NewClientBuilder().WithScheme(scheme)
	if len(existing) > 0 {
		builder = builder.WithObjects(existing...)
	}

	owner := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ssdproxy-test",
			Namespace: "default",
			UID:       "owner-uid",
		},
	}
	return &KernelizeOperation{
		client:         builder.Build(),
		scheme:         scheme,
		recorder:       events.NewFakeRecorder(10),
		owner:          owner,
		nodeName:       "test-node",
		image:          "quay.io/weka.io/weka-in-container:4.5.0",
		pullSecret:     "pull-secret",
		serviceAccount: "weka-sa",
		tolerations:    []corev1.Toleration{{Key: "gpu", Operator: corev1.TolerationOpExists}},
	}
}

func TestKernelizeEnsureContainer_CreatesExpectedSpec(t *testing.T) {
	scheme := newKernelizeTestScheme(t)
	op := newKernelizeTestOp(scheme)

	if err := op.EnsureContainer(context.Background()); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if op.container == nil {
		t.Fatal("container must be set after EnsureContainer")
	}

	c := op.container
	if c.Name != "weka-kernelize-test-node" {
		t.Errorf("Name = %q, want weka-kernelize-test-node", c.Name)
	}
	if c.Spec.Mode != weka.WekaContainerModeAdhocOp {
		t.Errorf("Mode = %q, want %q", c.Spec.Mode, weka.WekaContainerModeAdhocOp)
	}
	if !c.Spec.HostPID {
		t.Error("HostPID must be true")
	}
	if c.Spec.NodeAffinity != op.nodeName {
		t.Errorf("NodeAffinity = %q, want %q", c.Spec.NodeAffinity, op.nodeName)
	}
	if c.Spec.Image != op.image {
		t.Errorf("Image = %q, want %q", c.Spec.Image, op.image)
	}
	if c.Spec.Instructions == nil || c.Spec.Instructions.Type != weka.InstructionTypeKernelize {
		t.Errorf("Instructions.Type = %+v, want %q", c.Spec.Instructions, weka.InstructionTypeKernelize)
	}
	if len(c.OwnerReferences) != 1 || c.OwnerReferences[0].UID != op.owner.UID {
		t.Errorf("OwnerReferences = %+v, want a controller ref to owner UID %q", c.OwnerReferences, op.owner.UID)
	}

	got := &weka.WekaContainer{}
	if err := op.client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: c.Name}, got); err != nil {
		t.Fatalf("container must be persisted: %v", err)
	}
}

func TestKernelizeEnsureContainer_EmptyImageFailsWithoutCreating(t *testing.T) {
	scheme := newKernelizeTestScheme(t)
	op := newKernelizeTestOp(scheme)
	op.image = ""

	if err := op.EnsureContainer(context.Background()); err == nil {
		t.Fatal("expected an error for empty image")
	}
	if op.container != nil {
		t.Error("container must not be set on failure")
	}

	list := &weka.WekaContainerList{}
	if err := op.client.List(context.Background(), list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Errorf("expected no container to be created, got %d", len(list.Items))
	}
}

func TestKernelizePollResults_NoResultYetWaits(t *testing.T) {
	scheme := newKernelizeTestScheme(t)
	op := newKernelizeTestOp(scheme)
	op.container = &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "weka-kernelize-test-node",
			Namespace:         "default",
			CreationTimestamp: metav1.Now(),
		},
	}

	err := op.PollResults(context.Background())
	waitErr := &lifecycle.WaitError{}
	if !errors.As(err, &waitErr) {
		t.Fatalf("expected WaitError, got %v", err)
	}
}

func TestKernelizePollResults_StaleContainerCleansUp(t *testing.T) {
	scheme := newKernelizeTestScheme(t)
	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "weka-kernelize-test-node",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * kernelizeStaleTimeout)),
		},
	}
	op := newKernelizeTestOp(scheme, container)
	op.container = container

	if err := op.PollResults(context.Background()); err != nil {
		t.Fatalf("stale timeout must not block the caller, got %v", err)
	}
	if op.container != nil {
		t.Error("stale container must be cleared")
	}

	got := &weka.WekaContainer{}
	getErr := op.client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: container.Name}, got)
	if !apierrors.IsNotFound(getErr) {
		t.Fatalf("stale container must be deleted, got %v", getErr)
	}

	recorder := op.recorder.(*events.FakeRecorder)
	select {
	case event := <-recorder.Events:
		if !strings.Contains(event, "KernelizeFailed") {
			t.Errorf("event = %q, want a KernelizeFailed warning", event)
		}
	default:
		t.Error("expected a Warning event for the stale container, got none")
	}
}

func TestKernelizeProcessResult_RecordsEvent(t *testing.T) {
	// ProcessResult never returns an error on a failed result: failure must not block proxy pod creation.
	cases := []struct {
		name, result, wantEvent string
	}{
		{"success", `{"candidates":2,"filtered_out_in_use":1,"recovered":1,"failed":0}`, "Kernelized"},
		{"failed count", `{"candidates":1,"failed":1}`, "KernelizeFailed"},
		{"err field", `{"err":"weka-sign-drive exited 1"}`, "KernelizeFailed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := newKernelizeTestScheme(t)
			container := &weka.WekaContainer{
				ObjectMeta: metav1.ObjectMeta{Name: "weka-kernelize-test-node", Namespace: "default"},
				Status:     weka.WekaContainerStatus{ExecutionResult: &tc.result},
			}
			op := newKernelizeTestOp(scheme, container)
			op.container = container

			if err := op.ProcessResult(context.Background()); err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}

			recorder := op.recorder.(*events.FakeRecorder)
			select {
			case event := <-recorder.Events:
				if !strings.Contains(event, tc.wantEvent) {
					t.Errorf("event = %q, want %s", event, tc.wantEvent)
				}
			default:
				t.Errorf("expected a %s event, got none", tc.wantEvent)
			}
		})
	}
}

// testdata/kernelize_result.json is results.json as weka_runtime.py's kernelize_drives() writes
// it, with "raw" holding real `weka-sign-drive kernelize -J` output from the lab.
func TestKernelizeResult_ParsesRuntimeFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/kernelize_result.json")
	if err != nil {
		t.Fatal(err)
	}
	var result KernelizeResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var raw struct {
		Summary struct {
			TotalCandidates  int `json:"total_candidates"`
			FilteredOutInUse int `json:"filtered_out_in_use"`
			Recovered        int `json:"recovered"`
			Failed           int `json:"failed"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(result.Raw, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if result.Err != "" || result.Candidates != 4 || result.FilteredOutInUse != 3 || result.Recovered != 1 ||
		result.Candidates != raw.Summary.TotalCandidates ||
		result.FilteredOutInUse != raw.Summary.FilteredOutInUse || result.Recovered != raw.Summary.Recovered ||
		result.Failed != raw.Summary.Failed {
		t.Errorf("result = %+v, want counts matching raw summary %+v", result, raw.Summary)
	}
}

func TestKernelizeGetContainer_ForeignOwnerIsDeletedAndWaits(t *testing.T) {
	scheme := newKernelizeTestScheme(t)
	isController := true
	stale := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "weka-kernelize-test-node",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "weka.weka.io/v1alpha1", Kind: "WekaContainer",
				Name: "old-proxy", UID: "old-owner-uid", Controller: &isController,
			}},
		},
	}
	op := newKernelizeTestOp(scheme, stale)

	err := op.GetContainer(context.Background())
	waitErr := &lifecycle.WaitError{}
	if !errors.As(err, &waitErr) {
		t.Fatalf("expected WaitError, got %v", err)
	}
	if op.container != nil {
		t.Errorf("stale container must not be adopted")
	}
	got := &weka.WekaContainer{}
	getErr := op.client.Get(context.Background(), client.ObjectKeyFromObject(stale), got)
	if !apierrors.IsNotFound(getErr) {
		t.Errorf("stale container should be deleted, got err=%v", getErr)
	}
}

func TestKernelizeGetContainer_OwnContainerIsAdopted(t *testing.T) {
	scheme := newKernelizeTestScheme(t)
	isController := true
	own := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "weka-kernelize-test-node",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "weka.weka.io/v1alpha1", Kind: "WekaContainer",
				Name: "ssdproxy-test", UID: "owner-uid", Controller: &isController,
			}},
		},
	}
	op := newKernelizeTestOp(scheme, own)

	if err := op.GetContainer(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if op.container == nil {
		t.Errorf("own container should be adopted")
	}
}

func ownedKernelizeContainer(annotations map[string]string) *weka.WekaContainer {
	isController := true
	return &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "weka-kernelize-test-node",
			Namespace:   "default",
			Annotations: annotations,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "weka.weka.io/v1alpha1", Kind: "WekaContainer",
				Name: "ssdproxy-test", UID: "owner-uid", Controller: &isController,
			}},
		},
	}
}

func getKernelizeContainer(t *testing.T, c client.Client) (*weka.WekaContainer, error) {
	t.Helper()
	got := &weka.WekaContainer{}
	err := c.Get(context.Background(), client.ObjectKey{Name: "weka-kernelize-test-node", Namespace: "default"}, got)
	return got, err
}

func TestKernelizeProcessResult_MarksProcessed(t *testing.T) {
	scheme := newKernelizeTestScheme(t)
	result := `{"candidates":1,"filtered_out_in_use":1}`
	container := ownedKernelizeContainer(nil)
	container.Status.ExecutionResult = &result
	op := newKernelizeTestOp(scheme, container)
	op.container = container

	if err := op.ProcessResult(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := getKernelizeContainer(t, op.client)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Annotations[annotationKernelizeProcessedAt]; !ok {
		t.Errorf("processed annotation not set: %v", got.Annotations)
	}
}

func TestKernelizeOperation_ProcessedContainerIsReusedWithoutRerun(t *testing.T) {
	scheme := newKernelizeTestScheme(t)
	result := `{"candidates":1,"filtered_out_in_use":1}`
	container := ownedKernelizeContainer(map[string]string{annotationKernelizeProcessedAt: time.Now().UTC().Format(time.RFC3339)})
	container.Status.ExecutionResult = &result
	op := newKernelizeTestOp(scheme, container)

	engine := lifecycle.StepsEngine{Steps: op.GetSteps()}
	if err := engine.Run(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := getKernelizeContainer(t, op.client); err != nil {
		t.Errorf("processed container must be kept, got %v", err)
	}
	select {
	case event := <-op.recorder.(*events.FakeRecorder).Events:
		t.Errorf("reuse must not record an event, got %q", event)
	default:
	}
}

func TestKernelizeGetContainer_ConsumedIsDeletedAndWaits(t *testing.T) {
	scheme := newKernelizeTestScheme(t)
	container := ownedKernelizeContainer(map[string]string{
		annotationKernelizeProcessedAt: time.Now().UTC().Format(time.RFC3339),
		annotationKernelizeConsumedAt:  time.Now().UTC().Format(time.RFC3339),
	})
	op := newKernelizeTestOp(scheme, container)

	err := op.GetContainer(context.Background())
	waitErr := &lifecycle.WaitError{}
	if !errors.As(err, &waitErr) {
		t.Fatalf("expected WaitError, got %v", err)
	}
	if op.container != nil {
		t.Errorf("consumed container must not be adopted")
	}
	if _, err := getKernelizeContainer(t, op.client); !apierrors.IsNotFound(err) {
		t.Errorf("consumed container should be deleted, got err=%v", err)
	}
}

func TestFinalizeKernelize(t *testing.T) {
	owner := &weka.WekaContainer{ObjectMeta: metav1.ObjectMeta{Name: "ssdproxy-test", Namespace: "default", UID: "owner-uid"}}
	ctx := context.Background()

	t.Run("no container is a no-op", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(newKernelizeTestScheme(t)).Build()
		if err := FinalizeKernelize(ctx, c, owner, "test-node"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("marks consumed", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(newKernelizeTestScheme(t)).WithObjects(ownedKernelizeContainer(nil)).Build()
		if err := FinalizeKernelize(ctx, c, owner, "test-node"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, err := getKernelizeContainer(t, c)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got.Annotations[annotationKernelizeConsumedAt]; !ok {
			t.Errorf("consumed annotation not set: %v", got.Annotations)
		}
	})

	t.Run("kept within retention", func(t *testing.T) {
		recent := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
		c := fake.NewClientBuilder().WithScheme(newKernelizeTestScheme(t)).
			WithObjects(ownedKernelizeContainer(map[string]string{annotationKernelizeConsumedAt: recent})).Build()
		if err := FinalizeKernelize(ctx, c, owner, "test-node"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := getKernelizeContainer(t, c); err != nil {
			t.Errorf("container must be kept within retention, got %v", err)
		}
	})

	t.Run("deleted after retention", func(t *testing.T) {
		old := time.Now().Add(-kernelizeRetention - time.Minute).UTC().Format(time.RFC3339)
		c := fake.NewClientBuilder().WithScheme(newKernelizeTestScheme(t)).
			WithObjects(ownedKernelizeContainer(map[string]string{annotationKernelizeConsumedAt: old})).Build()
		if err := FinalizeKernelize(ctx, c, owner, "test-node"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := getKernelizeContainer(t, c); !apierrors.IsNotFound(err) {
			t.Errorf("container should be deleted after retention, got err=%v", err)
		}
	})

	t.Run("foreign-owned container is left alone", func(t *testing.T) {
		foreign := ownedKernelizeContainer(nil)
		foreign.OwnerReferences[0].UID = "old-owner-uid"
		c := fake.NewClientBuilder().WithScheme(newKernelizeTestScheme(t)).WithObjects(foreign).Build()
		if err := FinalizeKernelize(ctx, c, owner, "test-node"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, err := getKernelizeContainer(t, c)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got.Annotations[annotationKernelizeConsumedAt]; ok {
			t.Errorf("foreign container must not be marked consumed")
		}
	})
}
