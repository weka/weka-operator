package domain

import (
	"encoding/json"
	"testing"
)

func TestFeatureFlagsUnmarshalPerContainerDriverInterface(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     bool
	}{
		{"current key", `{"allow_per_container_driver_interface":true}`, true},
		{"legacy plural key", `{"allow_per_container_driver_interfaces":true}`, true},
		{"legacy plural false", `{"allow_per_container_driver_interfaces":false}`, false},
		{"absent", `{"wekactl_as_default":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f FeatureFlags
			if err := json.Unmarshal([]byte(tc.in), &f); err != nil {
				t.Fatal(err)
			}
			if f.AllowPerContainerDriverInterfaces != tc.want {
				t.Fatalf("got %v want %v", f.AllowPerContainerDriverInterfaces, tc.want)
			}
		})
	}
}
