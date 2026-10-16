package wekacontainer

import (
	"testing"

	v1 "k8s.io/api/core/v1"
)

func TestSharedCPUsReason(t *testing.T) {
	tests := []struct {
		qos    v1.PodQOSClass
		policy string
		want   string
	}{
		{v1.PodQOSGuaranteed, "static", ""},
		{v1.PodQOSGuaranteed, "", ""},
		{v1.PodQOSBurstable, "static", "PodNotGuaranteed"},
		{v1.PodQOSGuaranteed, "none", "NoStaticCPUManager"},
	}
	for _, tt := range tests {
		if got, _ := sharedCPUsReason(&v1.Pod{Status: v1.PodStatus{QOSClass: tt.qos}}, tt.policy); got != tt.want {
			t.Errorf("qos=%s policy=%q: reason = %q, want %q", tt.qos, tt.policy, got, tt.want)
		}
	}
}
