package operations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weka/go-steps-engine/lifecycle"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
)

func newPollTestOp(t *testing.T, age time.Duration, result *string) (*DiscoverNodeOperation, *record.FakeRecorder) {
	t.Helper()
	recorder := record.NewFakeRecorder(10)
	owner := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "owner-container", Namespace: "weka-operator-system"},
	}
	dsc := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "weka-dsc-node-1",
			Namespace:         "weka-operator-system",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		},
		Status: weka.WekaContainerStatus{ExecutionResult: result},
	}
	return &DiscoverNodeOperation{
		nodeName:  "node-1",
		container: dsc,
		ownerRef:  owner,
		recorder:  recorder,
	}, recorder
}

func waitErrorFrom(t *testing.T, err error) *lifecycle.WaitError {
	t.Helper()
	if err == nil {
		t.Fatal("expected a WaitError, got nil")
	}
	var waitErr *lifecycle.WaitError
	if !errors.As(err, &waitErr) {
		t.Fatalf("expected *lifecycle.WaitError, got %T: %v", err, err)
	}
	return waitErr
}

func assertNoEvents(t *testing.T, recorder *record.FakeRecorder) {
	t.Helper()
	select {
	case msg := <-recorder.Events:
		t.Fatalf("expected no events, got: %s", msg)
	default:
	}
}

func TestPollResultsReady(t *testing.T) {
	result := "{}"
	op, recorder := newPollTestOp(t, 30*time.Minute, &result)
	if err := op.PollResults(context.Background()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	assertNoEvents(t, recorder)
}

func TestPollResultsYoungContainerFastRequeue(t *testing.T) {
	op, recorder := newPollTestOp(t, 30*time.Second, nil)
	waitErr := waitErrorFrom(t, op.PollResults(context.Background()))
	if waitErr.Duration != 0 {
		t.Fatalf("expected default (0) duration within grace window, got %v", waitErr.Duration)
	}
	assertNoEvents(t, recorder)
}

func TestPollResultsPastGraceSlowRequeue(t *testing.T) {
	op, recorder := newPollTestOp(t, 5*time.Minute, nil)
	waitErr := waitErrorFrom(t, op.PollResults(context.Background()))
	if waitErr.Duration != discoverySlowRequeue {
		t.Fatalf("expected slow requeue %v, got %v", discoverySlowRequeue, waitErr.Duration)
	}
	assertNoEvents(t, recorder)
}

func TestPollResultsAtGraceBoundarySlowRequeue(t *testing.T) {
	op, recorder := newPollTestOp(t, discoveryWaitGracePeriod, nil)
	waitErr := waitErrorFrom(t, op.PollResults(context.Background()))
	if waitErr.Duration != discoverySlowRequeue {
		t.Fatalf("expected slow requeue at exactly the grace boundary, got %v", waitErr.Duration)
	}
	assertNoEvents(t, recorder)
}

func TestPollResultsAtStuckBoundaryEmitsEvent(t *testing.T) {
	op, recorder := newPollTestOp(t, discoveryStuckEventThreshold, nil)
	waitErrorFrom(t, op.PollResults(context.Background()))
	select {
	case <-recorder.Events:
	default:
		t.Fatal("expected a NodeDiscoveryStuck event at exactly the stuck threshold, got none")
	}
}

func TestPollResultsStuckEmitsEvent(t *testing.T) {
	op, recorder := newPollTestOp(t, 15*time.Minute, nil)
	waitErr := waitErrorFrom(t, op.PollResults(context.Background()))
	if waitErr.Duration != discoverySlowRequeue {
		t.Fatalf("expected slow requeue %v, got %v", discoverySlowRequeue, waitErr.Duration)
	}
	select {
	case msg := <-recorder.Events:
		if !strings.Contains(msg, "NodeDiscoveryStuck") || !strings.Contains(msg, "Warning") {
			t.Fatalf("expected NodeDiscoveryStuck warning event, got: %s", msg)
		}
		if !strings.Contains(msg, "weka-dsc-node-1") {
			t.Fatalf("expected event to name the dsc container, got: %s", msg)
		}
	default:
		t.Fatal("expected a NodeDiscoveryStuck event, got none")
	}
}
