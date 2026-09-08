package util

import (
	"fmt"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
)

// fakeEventRecorder captures the arguments of the last Eventf call.
type fakeEventRecorder struct {
	regarding runtime.Object
	note      string
}

func (f *fakeEventRecorder) Eventf(regarding runtime.Object, related runtime.Object, eventtype, reason, action, note string, args ...interface{}) {
	f.regarding = regarding
	f.note = fmt.Sprintf(note, args...)
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("failed to build clientgoscheme: %v", err)
	}
	if err := weka.AddToScheme(s); err != nil {
		t.Fatalf("failed to add weka scheme: %v", err)
	}
	return s
}

func TestWrapEventRecorder(t *testing.T) {
	rec := &fakeEventRecorder{}
	wrapped := WrapEventRecorder(rec, testScheme(t))
	obj := &corev1.ConfigMap{}
	obj.SetName("my-config")
	obj.SetNamespace("my-namespace")
	obj.SetResourceVersion("12345")

	RecordEvent(wrapped, obj, corev1.EventTypeNormal, "Reason", "Action", "hello")

	ref, ok := rec.regarding.(*corev1.ObjectReference)
	if !ok {
		t.Fatalf("expected regarding object to be *corev1.ObjectReference, got %T", rec.regarding)
	}
	if ref.ResourceVersion != "" {
		t.Errorf("expected captured ResourceVersion to be stripped, got %q", ref.ResourceVersion)
	}
	if ref.FieldPath == "" {
		t.Error("expected FieldPath to carry the message digest, got empty string")
	}
	if ref.Name != "my-config" {
		t.Errorf("expected Name %q copied from object, got %q", "my-config", ref.Name)
	}
	if ref.Namespace != "my-namespace" {
		t.Errorf("expected Namespace %q copied from object, got %q", "my-namespace", ref.Namespace)
	}
	if obj.ResourceVersion != "12345" {
		t.Errorf("RecordEvent must not mutate the caller's object, got ResourceVersion %q", obj.ResourceVersion)
	}
	if rec.note != "hello" {
		t.Errorf("expected note %q, got %q", "hello", rec.note)
	}
}

func TestWrapEventRecorder_FieldPathDigestDistinguishesMessages(t *testing.T) {
	rec := &fakeEventRecorder{}
	wrapped := WrapEventRecorder(rec, testScheme(t))
	obj := &corev1.ConfigMap{}
	obj.SetName("my-config")
	obj.SetNamespace("my-namespace")

	RecordEvent(wrapped, obj, corev1.EventTypeNormal, "Reason", "Action", "first message")
	fieldPathA := rec.regarding.(*corev1.ObjectReference).FieldPath

	RecordEvent(wrapped, obj, corev1.EventTypeNormal, "Reason", "Action", "first message")
	fieldPathASame := rec.regarding.(*corev1.ObjectReference).FieldPath
	if fieldPathA != fieldPathASame {
		t.Errorf("expected identical messages to produce the same FieldPath, got %q and %q", fieldPathA, fieldPathASame)
	}

	RecordEvent(wrapped, obj, corev1.EventTypeNormal, "Reason", "Action", "second message")
	fieldPathB := rec.regarding.(*corev1.ObjectReference).FieldPath
	if fieldPathA == fieldPathB {
		t.Errorf("expected different messages to produce different FieldPaths, both were %q", fieldPathA)
	}
}

func TestWrapEventRecorder_ResolvesWekaCRDKind(t *testing.T) {
	rec := &fakeEventRecorder{}
	wrapped := WrapEventRecorder(rec, testScheme(t))
	obj := &weka.WekaContainer{}
	obj.SetName("my-container")
	obj.SetNamespace("my-namespace")

	RecordEvent(wrapped, obj, corev1.EventTypeNormal, "Reason", "Action", "hello")

	ref, ok := rec.regarding.(*corev1.ObjectReference)
	if !ok {
		t.Fatalf("expected regarding object to be *corev1.ObjectReference, got %T", rec.regarding)
	}
	if ref.Kind != "WekaContainer" {
		t.Errorf("expected scheme to resolve Kind %q, got %q", "WekaContainer", ref.Kind)
	}
}

func TestWrapEventRecorder_TruncatesLongMessageAndKeepsShortOne(t *testing.T) {
	rec := &fakeEventRecorder{}
	wrapped := WrapEventRecorder(rec, testScheme(t))
	obj := &corev1.ConfigMap{}

	RecordEvent(wrapped, obj, corev1.EventTypeNormal, "Reason", "Action", strings.Repeat("a", 2000))
	if len(rec.note) > maxEventNoteBytes {
		t.Errorf("expected note truncated to at most %d bytes, got %d", maxEventNoteBytes, len(rec.note))
	}
	if !strings.HasSuffix(rec.note, "...") {
		t.Errorf("expected truncated note to end with %q, got %q", "...", rec.note)
	}

	RecordEvent(wrapped, obj, corev1.EventTypeNormal, "Reason", "Action", "short message")
	if rec.note != "short message" {
		t.Errorf("expected short message to pass through unchanged, got %q", rec.note)
	}
}

func TestWrapEventRecorder_ObjectReferenceRegardingNotMutated(t *testing.T) {
	rec := &fakeEventRecorder{}
	wrapped := WrapEventRecorder(rec, testScheme(t))
	ref := &corev1.ObjectReference{
		Kind:            "ConfigMap",
		Name:            "my-config",
		Namespace:       "my-namespace",
		ResourceVersion: "12345",
		FieldPath:       "original-field-path",
	}

	RecordEvent(wrapped, ref, corev1.EventTypeNormal, "Reason", "Action", "hello")

	if ref.ResourceVersion != "12345" {
		t.Errorf("expected caller's ObjectReference ResourceVersion untouched, got %q", ref.ResourceVersion)
	}
	if ref.FieldPath != "original-field-path" {
		t.Errorf("expected caller's ObjectReference FieldPath untouched, got %q", ref.FieldPath)
	}
}

func TestRecordEvent_NilRecorderDoesNotPanic(t *testing.T) {
	RecordEvent(nil, &corev1.ConfigMap{}, corev1.EventTypeNormal, "Reason", "Action", "hello")
}
