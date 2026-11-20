package operations

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/kubernetes"
)

func npn(ownerKind, ownerName, state string, macs ...string) unstructured.Unstructured {
	vnics := make([]any, 0, len(macs))
	for _, m := range macs {
		vnics = append(vnics, map[string]any{"macAddress": m, "vnicId": "ocid1.vnic.x"})
	}
	return unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":            ownerName,
			"ownerReferences": []any{map[string]any{"apiVersion": "v1", "kind": ownerKind, "name": ownerName, "uid": "u"}},
		},
		"status": map[string]any{"state": state, "vnics": vnics},
	}}
}

func TestPodVNICMACsByNode(t *testing.T) {
	cases := map[string]struct {
		items []unstructured.Unstructured
		want  map[string][]string
	}{
		"no npn": {nil, nil},
		"two nodes": {
			[]unstructured.Unstructured{npn("Node", "n1", "SUCCESS", "02:00:17:00:00:01", "02:00:17:00:00:02"), npn("Node", "n2", "SUCCESS", "02:00:17:00:00:03")},
			map[string][]string{"n1": {"02:00:17:00:00:01", "02:00:17:00:00:02"}, "n2": {"02:00:17:00:00:03"}},
		},
		"non-node owner ignored": {
			[]unstructured.Unstructured{npn("Pod", "p1", "SUCCESS", "02:00:17:00:00:04")},
			map[string][]string{},
		},
		"pending node absent, ready node without vnics present": {
			[]unstructured.Unstructured{npn("Node", "n1", "IN_PROGRESS", "02:00:17:00:00:05"), npn("Node", "n2", "SUCCESS")},
			map[string][]string{"n2": {}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := podVNICMACsByNode(tc.items); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

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
	owner    *weka.WekaManualOperation
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
	objs := []client.Object{owner}
	var containers []*weka.WekaContainer
	for node, res := range results {
		objs = append(objs, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: node}})
		ctr := ensureNICsContainer(node, res)
		objs = append(objs, ctr)
		containers = append(containers, ctr)
	}
	c := ensureNICsFakeClient(t, objs...)
	h := &ensureNICsHarness{c: c, owner: owner, recorder: events.NewFakeRecorder(10)}
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

func TestEnsureNICsRetryable(t *testing.T) {
	cases := map[string]struct {
		res  ensureNICsResult
		want bool
	}{
		"oci 409":            {ensureNICsResult{Err: "AttachNicError: Error returned by AttachVnic operation. Http Status Code: 409. Error Code: IncorrectState. Message: Instance is currently being modified, try again later"}, true},
		"429":                {ensureNICsResult{Err: "Http Status Code: 429"}, true},
		"throttling":         {ensureNICsResult{Err: "Throttling: Rate exceeded"}, true},
		"503":                {ensureNICsResult{Err: "status 503 ServiceUnavailable"}, true},
		"flag":               {ensureNICsResult{Err: "anything", Retryable: true}, true},
		"max nics":           {ensureNICsResult{Err: "MaxNicsExceededError: too many"}, false},
		"port number":        {ensureNICsResult{Err: "bad port 14090"}, false},
		"aws 503":            {ensureNICsResult{Err: "operation error EC2: AttachNetworkInterface, https response error StatusCode: 503, RequestID: x"}, true},
		"number not a code":  {ensureNICsResult{Err: "CreateNicError: limit of 500 exceeded"}, false},
		"unknown":            {ensureNICsResult{Err: "boom"}, false},
		"flag without error": {ensureNICsResult{Retryable: true}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.res.isRetryable(); got != tc.want {
				t.Fatalf("isRetryable = %v, want %v", got, tc.want)
			}
		})
	}
}

func attemptsOf(t *testing.T, owner client.Object) map[string]nodeAttempts {
	t.Helper()
	got := map[string]nodeAttempts{}
	if raw := owner.GetAnnotations()[ensureNICsAttemptsAnnotation]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
	}
	return got
}

func setAttempts(t *testing.T, h *ensureNICsHarness, node string, a nodeAttempts) {
	t.Helper()
	raw, err := json.Marshal(map[string]nodeAttempts{node: a})
	if err != nil {
		t.Fatal(err)
	}
	h.owner.Annotations = map[string]string{ensureNICsAttemptsAnnotation: string(raw)}
	if err := h.c.Update(context.Background(), h.owner); err != nil {
		t.Fatal(err)
	}
}

type ensureNICsFakeManager struct {
	ctrl.Manager
	client client.Client
}

func (m *ensureNICsFakeManager) GetAPIReader() client.Reader { return m.client }

func TestEnsureNICsEnforcesRetryAfter(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		retryAfter time.Time
		wantWait   bool
	}{
		"blocked before retryAfter": {time.Now().Add(time.Minute), true},
		"allowed after retryAfter":  {time.Now().Add(-time.Second), false},
	} {
		t.Run(name, func(t *testing.T) {
			h := newEnsureNICsHarness(t, "Running", map[string]*string{"node-a": nil})
			if err := h.c.DeleteAllOf(ctx, &weka.WekaContainer{}, client.InNamespace("default")); err != nil {
				t.Fatal(err)
			}
			setAttempts(t, h, "node-a", nodeAttempts{Attempts: 1, RetryAfter: tc.retryAfter})
			h.op.containers = nil
			h.op.payload = &weka.EnsureNICsPayload{}
			h.op.scheme = h.c.Scheme()
			h.op.mgr = &ensureNICsFakeManager{client: h.c}

			err := h.op.EnsureContainers(ctx)

			var we *lifecycle.WaitError
			if tc.wantWait {
				if !errors.As(err, &we) || we.Duration <= 0 || we.Duration > time.Minute {
					t.Fatalf("err = %v, want wait up to 1m", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want := 1
			if tc.wantWait {
				want = 0
			}
			if n := remainingContainers(t, h.c); n != want {
				t.Errorf("containers = %d, want %d", n, want)
			}
		})
	}
}

func TestEnsureNICsRetry(t *testing.T) {
	ctx := context.Background()
	transient := func(t *testing.T) *string {
		return ensureNICsJSON(t, ensureNICsResult{Err: "AttachNicError: 409 IncorrectState, try again later"})
	}
	okRes := ensureNICsJSON(t, ensureNICsResult{Ensured: true, NICs: []domain.NIC{{}}})

	t.Run("retries only the transient node", func(t *testing.T) {
		h := newEnsureNICsHarness(t, "Running", map[string]*string{"node-a": transient(t), "node-b": okRes})

		err := h.op.ProcessResult(ctx)

		var we *lifecycle.WaitError
		if !errors.As(err, &we) {
			t.Fatalf("want WaitError, got %v", err)
		}
		if h.failed != 0 {
			t.Error("failure callback called while retrying")
		}
		ev := <-h.recorder.Events
		if !strings.Contains(ev, "EnsureNICsRetrying") || !strings.Contains(ev, "node node-a: attempt 1/10") {
			t.Errorf("unexpected event %q", ev)
		}
		var list weka.WekaContainerList
		if err := h.c.List(ctx, &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Items) != 1 || list.Items[0].Spec.NodeAffinity != "node-b" {
			t.Errorf("containers = %v, want only node-b", list.Items)
		}
		if got := attemptsOf(t, h.owner)["node-a"]; got.Attempts != 1 || time.Until(got.RetryAfter) <= 0 {
			t.Errorf("attempts = %+v, want 1 attempt with retryAfter in the future", got)
		}
		var stored weka.WekaManualOperation
		if err := h.c.Get(ctx, client.ObjectKeyFromObject(h.owner), &stored); err != nil {
			t.Fatal(err)
		}
		if got := attemptsOf(t, &stored)["node-a"]; got.Attempts != 1 {
			t.Errorf("annotation not persisted: %v", stored.Annotations)
		}
	})

	t.Run("backoff doubles and is capped", func(t *testing.T) {
		for attempts, want := range map[int]time.Duration{0: 15 * time.Second, 2: 60 * time.Second, 5: 2 * time.Minute} {
			h := newEnsureNICsHarness(t, "Running", map[string]*string{"node-a": transient(t)})
			setAttempts(t, h, "node-a", nodeAttempts{Attempts: attempts})
			var we *lifecycle.WaitError
			if err := h.op.ProcessResult(ctx); !errors.As(err, &we) || we.Duration != want {
				t.Fatalf("attempts=%d: err=%v, want wait %s", attempts, err, want)
			}
		}
	})

	t.Run("failed delete is retried without counting again", func(t *testing.T) {
		h := newEnsureNICsHarness(t, "Running", map[string]*string{"node-a": transient(t)})
		setAttempts(t, h, "node-a", nodeAttempts{Attempts: 1, RetryAfter: time.Now().Add(time.Minute)})

		var we *lifecycle.WaitError
		if err := h.op.ProcessResult(ctx); !errors.As(err, &we) {
			t.Fatalf("want WaitError, got %v", err)
		}
		if got := attemptsOf(t, h.owner)["node-a"]; got.Attempts != 1 {
			t.Errorf("attempts = %d, want 1", got.Attempts)
		}
		if n := remainingContainers(t, h.c); n != 0 {
			t.Errorf("containers left = %d, want 0", n)
		}
		select {
		case ev := <-h.recorder.Events:
			t.Errorf("unexpected event %q", ev)
		default:
		}
	})

	t.Run("gives up after max attempts", func(t *testing.T) {
		h := newEnsureNICsHarness(t, "Running", map[string]*string{"node-a": transient(t)})
		setAttempts(t, h, "node-a", nodeAttempts{Attempts: ensureNICsMaxAttempts})

		err := h.op.ProcessResult(ctx)

		var we *lifecycle.WaitError
		if !errors.As(err, &we) || h.failed != 1 {
			t.Fatalf("err=%v failed=%d, want terminal failure", err, h.failed)
		}
		if !strings.Contains(h.op.GetJsonResult(), "(gave up after 10 attempts)") {
			t.Errorf("result lacks suffix: %s", h.op.GetJsonResult())
		}
		if _, ok := h.owner.Annotations[ensureNICsAttemptsAnnotation]; ok {
			t.Error("attempts annotation not cleared")
		}
	})
}

func TestEnsureNICsWaitsForTerminatingContainers(t *testing.T) {
	h := newEnsureNICsHarness(t, "Running", map[string]*string{"node-a": nil})
	now := metav1.Now()
	h.op.containers[0].DeletionTimestamp = &now
	if err := h.op.WaitForTerminatingContainers(context.Background()); !errors.As(err, new(*lifecycle.WaitError)) {
		t.Fatalf("err = %v, want WaitError", err)
	}
	h.op.containers[0].DeletionTimestamp = nil
	if err := h.op.WaitForTerminatingContainers(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureNICsIsTerminal(t *testing.T) {
	cases := []struct {
		owner  client.Object
		status string
		want   bool
	}{
		{&weka.WekaManualOperation{}, "Done", true},
		{&weka.WekaManualOperation{}, "Failed", true},
		{&weka.WekaManualOperation{}, "Running", false},
		{&weka.WekaPolicy{}, "Done", true},
		{&weka.WekaPolicy{}, "Failed", false},
		{&weka.WekaPolicy{}, "Running", false},
	}
	for _, tc := range cases {
		op := &EnsureNICsOperation{ownerRef: tc.owner, ownerStatus: tc.status}
		if got := op.IsTerminal(); got != tc.want {
			t.Errorf("%T %s: IsTerminal = %v, want %v", tc.owner, tc.status, got, tc.want)
		}
	}
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

func TestEnsureNICsWaitRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name       string
		retryAfter time.Time
		backingOff bool
	}{
		{"future", time.Now().Add(time.Minute), true},
		{"past", time.Now().Add(-time.Minute), false},
		{"zero", time.Time{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newEnsureNICsHarness(t, "Running", map[string]*string{"node-a": nil})
			h.op.retryAfter = tc.retryAfter
			if got := h.op.isBackingOff(); got != tc.backingOff {
				t.Fatalf("isBackingOff = %v, want %v", got, tc.backingOff)
			}
			if !tc.backingOff {
				return
			}
			if err := h.op.WaitRetryAfter(context.Background()); !errors.As(err, new(*lifecycle.WaitError)) {
				t.Fatalf("err = %v, want WaitError", err)
			}
			if n := remainingContainers(t, h.c); n != 0 {
				t.Errorf("containers left = %d, want 0", n)
			}
		})
	}
}
