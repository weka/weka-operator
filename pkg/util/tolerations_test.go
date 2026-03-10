package util

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestCheckTolerations(t *testing.T) {
	tests := []struct {
		name        string
		taints      []corev1.Taint
		tolerations []corev1.Toleration
		ignoreKeys  []string
		expected    bool
	}{
		{
			name: "prefer no schedule taint does not block without toleration",
			taints: []corev1.Taint{
				{
					Key:    "example.com/prefer-only",
					Effect: corev1.TaintEffectPreferNoSchedule,
				},
			},
			expected: true,
		},
		{
			name: "no schedule taint blocks without toleration",
			taints: []corev1.Taint{
				{
					Key:    "example.com/dedicated",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
			expected: false,
		},
		{
			name: "no schedule taint passes with matching toleration",
			taints: []corev1.Taint{
				{
					Key:    "example.com/dedicated",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
			tolerations: []corev1.Toleration{
				{
					Key:      "example.com/dedicated",
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoSchedule,
				},
			},
			expected: true,
		},
		{
			name: "mixed taints pass when only hard taints are tolerated",
			taints: []corev1.Taint{
				{
					Key:    "example.com/prefer-only",
					Effect: corev1.TaintEffectPreferNoSchedule,
				},
				{
					Key:    "example.com/dedicated",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
			tolerations: []corev1.Toleration{
				{
					Key:      "example.com/dedicated",
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoSchedule,
				},
			},
			expected: true,
		},
		{
			name: "no execute taint blocks without toleration",
			taints: []corev1.Taint{
				{
					Key:    "example.com/evict",
					Effect: corev1.TaintEffectNoExecute,
				},
			},
			expected: false,
		},
		{
			name: "ignored taint key is skipped",
			taints: []corev1.Taint{
				{
					Key:    "example.com/ignored",
					Effect: corev1.TaintEffectNoExecute,
				},
			},
			ignoreKeys: []string{"example.com/ignored"},
			expected:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := CheckTolerations(tt.taints, tt.tolerations, tt.ignoreKeys)
			if actual != tt.expected {
				t.Fatalf("expected %v, got %v", tt.expected, actual)
			}
		})
	}
}
