package wekacontainer

import (
	"testing"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

func TestVidLost(t *testing.T) {
	cases := []struct {
		name string
		vd   weka.VirtualDrive
		want bool
	}{
		{"resolved VID", weka.VirtualDrive{Label: "L", VirtualUUID: "V"}, true},
		{"flag-off VID equals label", weka.VirtualDrive{Label: "L", VirtualUUID: "L"}, false},
		{"legacy VID only", weka.VirtualDrive{VirtualUUID: "V"}, false},
		{"pending", weka.VirtualDrive{Label: "L"}, false},
	}
	for _, c := range cases {
		if got := vidLost(&c.vd); got != c.want {
			t.Errorf("%s: vidLost = %v, want %v", c.name, got, c.want)
		}
	}
}
