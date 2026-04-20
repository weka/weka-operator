package util

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
)

// CheckTolerations Check if the given taints can be tolerated by the given tolerations.
func CheckTolerations(taints []corev1.Taint, tolerations []corev1.Toleration, ignoreTaints []string) bool {
TAINT:
	for _, taint := range taints {
		// PreferNoSchedule taints are soft scheduling hints and should not
		// be treated as hard blockers when selecting eligible nodes.
		if taint.Effect == corev1.TaintEffectPreferNoSchedule {
			continue
		}
		if ignoreTaints != nil && slices.Contains(ignoreTaints, taint.Key) {
			continue
		}
		for _, toleration := range tolerations {
			if toleration.ToleratesTaint(&taint) {
				continue TAINT
			}
		}
		return false
	}
	return true
}

// TolerationsEqual checks if two tolerations are equal
func TolerationsEqual(a, b corev1.Toleration) bool {
	return a.Key == b.Key &&
		a.Operator == b.Operator &&
		a.Value == b.Value &&
		a.Effect == b.Effect &&
		tolerationSecondsEqual(a.TolerationSeconds, b.TolerationSeconds)
}

// TolerationsEqualExceptSeconds checks if two tolerations are equal ignoring tolerationSeconds
func TolerationsEqualExceptSeconds(a, b corev1.Toleration) bool {
	return a.Key == b.Key &&
		a.Operator == b.Operator &&
		a.Value == b.Value &&
		a.Effect == b.Effect
}

func tolerationSecondsEqual(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
