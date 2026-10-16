package operations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/weka/go-steps-engine/lifecycle"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/kubernetes"
)

const ensureNICsOwnerUID = types.UID("owner-uid")

func ensureNICsFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&corev1.Node{}).
		WithIndex(&weka.WekaContainer{}, "metadata.ownerReferences.uid", func(o client.Object) []string {
			var uids []string
			for _, ref := range o.GetOwnerReferences() {
				uids = append(uids, string(ref.UID))
			}
			return uids
		}).Build()
}

func ensureNICsContainer(node string, result *string) *weka.WekaContainer {
	c := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "adhoc-" + node,
			Namespace: "default",
			Labels:    map[string]string{"weka.io/mode": weka.WekaContainerModeAdhocOpWC},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "weka.weka.io/v1alpha1", Kind: "WekaManualOperation", Name: "op", UID: ensureNICsOwnerUID,
			}},
		},
		Spec: weka.WekaContainerSpec{NodeAffinity: weka.NodeName(node)},
	}
	c.Status.ExecutionResult = result
	return c
}

type ensureNICsHarness struct {
	op       *EnsureNICsOperation
	c        client.Client
	recorder *events.FakeRecorder
	failed   int
	success  int
}

func newEnsureNICsHarness(t *testing.T, ownerStatus string, results map[string]*string) *ensureNICsHarness {
	t.Helper()
	owner := &weka.WekaManualOperation{
		ObjectMeta: metav1.ObjectMeta{Name: "op", Namespace: "default", UID: ensureNICsOwnerUID},
	}
	owner.Status.Status = ownerStatus
	var objs []client.Object
	var containers []*weka.WekaContainer
	for node, res := range results {
		objs = append(objs, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: node}})
		ctr := ensureNICsContainer(node, res)
		objs = append(objs, ctr)
		containers = append(containers, ctr)
	}
	c := ensureNICsFakeClient(t, objs...)
	h := &ensureNICsHarness{c: c, recorder: events.NewFakeRecorder(10)}
	h.op = &EnsureNICsOperation{
		client:          c,
		kubeService:     kubernetes.NewKubeService(c),
		ownerRef:        owner,
		ownerStatus:     ownerStatus,
		containers:      containers,
		recorder:        h.recorder,
		successCallback: func(context.Context) error { h.success++; return nil },
		failureCallback: func(context.Context) error { h.failed++; return nil },
	}
	return h
}

func ensureNICsJSON(t *testing.T, r ensureNICsResult) *string {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	return &s
}

func remainingContainers(t *testing.T, c client.Client) int {
	t.Helper()
	var list weka.WekaContainerList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	return len(list.Items)
}

func TestEnsureNICsProcessResult(t *testing.T) {
	ctx := context.Background()
	okRes := func(t *testing.T) *string {
		return ensureNICsJSON(t, ensureNICsResult{Ensured: true, NICs: []domain.NIC{{}, {}}})
	}
	errRes := func(t *testing.T) *string {
		return ensureNICsJSON(t, ensureNICsResult{Err: "MaxNicsExceededError: too many"})
	}

	t.Run("node error fails the operation", func(t *testing.T) {
		h := newEnsureNICsHarness(t, "Running", map[string]*string{"node-a": errRes(t)})

		err := h.op.ProcessResult(ctx)

		var we *lifecycle.WaitError
		if !errors.As(err, &we) {
			t.Fatalf("want WaitError, got %T: %v", err, err)
		}
		if h.failed != 1 || h.success != 0 {
			t.Errorf("failed=%d success=%d, want 1/0", h.failed, h.success)
		}
		select {
		case ev := <-h.recorder.Events:
			if !strings.Contains(ev, "Warning") || !strings.Contains(ev, "EnsureNICsFailed") || !strings.Contains(ev, "node node-a: MaxNicsExceededError") {
				t.Errorf("unexpected event %q", ev)
			}
		default:
			t.Fatal("no event recorded")
		}
		if n := remainingContainers(t, h.c); n != 0 {
			t.Errorf("containers left = %d, want 0", n)
		}
		if !strings.Contains(h.op.GetJsonResult(), "MaxNicsExceededError") {
			t.Errorf("result JSON lost the node error: %s", h.op.GetJsonResult())
		}
	})

	t.Run("unparsable result counts as an error", func(t *testing.T) {
		bad := "not json"
		h := newEnsureNICsHarness(t, "Running", map[string]*string{"node-a": &bad})
		if err := h.op.ProcessResult(ctx); err == nil || h.failed != 1 {
			t.Fatalf("err=%v failed=%d, want failure", err, h.failed)
		}
	})

	t.Run("all ok patches the node", func(t *testing.T) {
		h := newEnsureNICsHarness(t, "Running", map[string]*string{"node-a": okRes(t)})

		if err := h.op.ProcessResult(ctx); err != nil {
			t.Fatal(err)
		}
		if err := h.op.SuccessUpdate(ctx); err != nil {
			t.Fatal(err)
		}
		if h.success != 1 || h.failed != 0 {
			t.Errorf("success=%d failed=%d, want 1/0", h.success, h.failed)
		}
		var node corev1.Node
		if err := h.c.Get(ctx, types.NamespacedName{Name: "node-a"}, &node); err != nil {
			t.Fatal(err)
		}
		if node.Annotations[domain.WEKANICs] == "" {
			t.Error("node annotation not set")
		}
		qty := node.Status.Allocatable[domain.WEKANICs]
		if qty.Value() != 2 {
			t.Errorf("allocatable = %s, want 2", qty.String())
		}
	})

	t.Run("partial failure patches ok node and fails", func(t *testing.T) {
		h := newEnsureNICsHarness(t, "Running", map[string]*string{"node-a": okRes(t), "node-b": errRes(t)})

		err := h.op.ProcessResult(ctx)

		var we *lifecycle.WaitError
		if !errors.As(err, &we) {
			t.Fatalf("want WaitError, got %v", err)
		}
		if h.failed != 1 || h.success != 0 {
			t.Errorf("failed=%d success=%d, want 1/0", h.failed, h.success)
		}
		var node corev1.Node
		if err := h.c.Get(ctx, types.NamespacedName{Name: "node-a"}, &node); err != nil {
			t.Fatal(err)
		}
		if node.Annotations[domain.WEKANICs] == "" {
			t.Error("ok node not patched")
		}
		var bad corev1.Node
		if err := h.c.Get(ctx, types.NamespacedName{Name: "node-b"}, &bad); err != nil {
			t.Fatal(err)
		}
		if bad.Annotations[domain.WEKANICs] != "" {
			t.Error("failed node must not be patched")
		}
	})

	t.Run("waits while a result is missing", func(t *testing.T) {
		h := newEnsureNICsHarness(t, "Running", map[string]*string{"node-a": errRes(t), "node-b": nil})
		err := h.op.ProcessResult(ctx)
		var we *lifecycle.WaitError
		if !errors.As(err, &we) || h.failed != 0 {
			t.Fatalf("err=%v failed=%d, want wait without failure callback", err, h.failed)
		}
		select {
		case ev := <-h.recorder.Events:
			if !strings.Contains(ev, "Warning") || !strings.Contains(ev, "EnsureNICsFailed") || !strings.Contains(ev, "node node-a: MaxNicsExceededError") {
				t.Errorf("unexpected event %q", ev)
			}
		default:
			t.Fatal("no event recorded for failed node while another result is missing")
		}
	})

	t.Run("failure callback error keeps containers", func(t *testing.T) {
		h := newEnsureNICsHarness(t, "Running", map[string]*string{"node-a": errRes(t)})
		cbErr := errors.New("status write failed")
		h.op.failureCallback = func(context.Context) error { return cbErr }

		err := h.op.ProcessResult(ctx)

		if !errors.Is(err, cbErr) {
			t.Fatalf("err = %v, want %v", err, cbErr)
		}
		if n := remainingContainers(t, h.c); n != 1 {
			t.Errorf("containers left = %d, want 1", n)
		}
	})
}

func TestEnsureNICsSkipIfTerminal(t *testing.T) {
	for _, status := range []string{"Done", "Failed"} {
		t.Run(status, func(t *testing.T) {
			h := newEnsureNICsHarness(t, status, map[string]*string{"node-a": nil})
			// containers are discovered from the API, not the in-memory slice
			h.op.containers = nil

			engine := lifecycle.StepsEngine{Steps: h.op.GetSteps()}
			if err := engine.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if n := remainingContainers(t, h.c); n != 0 {
				t.Errorf("containers left = %d, want 0", n)
			}
			if h.success != 0 || h.failed != 0 {
				t.Errorf("callbacks called: success=%d failed=%d", h.success, h.failed)
			}
		})
	}
}
