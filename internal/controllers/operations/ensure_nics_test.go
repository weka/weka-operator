package operations

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
