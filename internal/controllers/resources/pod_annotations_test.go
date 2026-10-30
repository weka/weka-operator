package resources

import (
	"maps"
	"testing"
)

func TestAnnotationsForWekaPod(t *testing.T) {
	container := map[string]string{"a3": "3", "a1": "1", "a2": "2", "kubectl.kubernetes.io/last-applied-configuration": "{}"}

	t.Run("stable sorted value", func(t *testing.T) {
		first := AnnotationsForWekaPod(container, nil)
		if got := first[appliedAnnotationsKey]; got != "a1,a2,a3" {
			t.Fatalf("applied = %q, want a1,a2,a3", got)
		}
		for range 50 {
			if got := AnnotationsForWekaPod(container, nil); !maps.Equal(got, first) {
				t.Fatalf("output changed between calls: %v vs %v", got, first)
			}
		}
		if got := AnnotationsForWekaPod(container, first); !maps.Equal(got, first) {
			t.Fatalf("feeding output back changed it: %v vs %v", got, first)
		}
	})

	t.Run("removed key leaves pod and list, foreign keys kept", func(t *testing.T) {
		pod := map[string]string{"a1": "1", "a2": "2", "a3": "3", "foreign": "x", appliedAnnotationsKey: "a1,a2,a3"}
		got := AnnotationsForWekaPod(map[string]string{"a2": "2", "a3": "3"}, pod)
		want := map[string]string{"a2": "2", "a3": "3", "foreign": "x", appliedAnnotationsKey: "a2,a3"}
		if !maps.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("no container annotations drops the list", func(t *testing.T) {
		pod := map[string]string{"foreign": "x", appliedAnnotationsKey: "a1,a2,a3,a4,a5,a6"}
		got := AnnotationsForWekaPod(nil, pod)
		if want := map[string]string{"foreign": "x"}; !maps.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("key no longer applied and set by someone else is kept", func(t *testing.T) {
		pod := map[string]string{"a1": "theirs", "a2": "2", appliedAnnotationsKey: "a2"}
		got := AnnotationsForWekaPod(map[string]string{"a2": "2"}, pod)
		if want := map[string]string{"a1": "theirs", "a2": "2", appliedAnnotationsKey: "a2"}; !maps.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
}
