package utils

import (
	"testing"

	v1 "k8s.io/api/core/v1"
)

func TestPodUnschedulable(t *testing.T) {
	tests := []struct {
		name string
		pod  *v1.Pod
		want bool
	}{
		{
			name: "explicit Unschedulable condition",
			pod: &v1.Pod{Status: v1.PodStatus{Conditions: []v1.PodCondition{
				{Type: v1.PodScheduled, Status: v1.ConditionFalse, Reason: "Unschedulable"},
			}}},
			want: true,
		},
		{
			name: "not yet evaluated by the scheduler is not the same as unschedulable",
			pod: &v1.Pod{Status: v1.PodStatus{Conditions: []v1.PodCondition{
				{Type: v1.PodScheduled, Status: v1.ConditionFalse, Reason: "SchedulingGated"},
			}}},
			want: false,
		},
		{
			name: "PodScheduled true",
			pod: &v1.Pod{Status: v1.PodStatus{Conditions: []v1.PodCondition{
				{Type: v1.PodScheduled, Status: v1.ConditionTrue},
			}}},
			want: false,
		},
		{
			name: "no conditions",
			pod:  &v1.Pod{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PodUnschedulable(tt.pod); got != tt.want {
				t.Errorf("PodUnschedulable() = %v, want %v", got, tt.want)
			}
		})
	}
}
