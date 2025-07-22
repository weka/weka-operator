package util

import (
	"fmt"
	"reflect"
	"slices"
	"sort"

	corev1 "k8s.io/api/core/v1"
)

// CheckTolerations Check if the given taints can be tolerated by the given tolerations.
func CheckTolerations(taints []corev1.Taint, tolerations []corev1.Toleration, ignoreTaints []string) bool {
TAINT:
	for _, taint := range taints {
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

// CompareTolerations check if the given tolerations are equal
func CompareTolerations(a, b []corev1.Toleration, ignoreUnhealthy bool) bool {
	if ignoreUnhealthy {
		a = filterUnhealthyDefaultTolerations(a)
		b = filterUnhealthyDefaultTolerations(b)
	}

	aNorm := normalizeTolerations(a)
	bNorm := normalizeTolerations(b)
	return reflect.DeepEqual(aNorm, bNorm)
}

func filterUnhealthyDefaultTolerations(tolerations []corev1.Toleration) []corev1.Toleration {
	var filtered []corev1.Toleration
	for _, toleration := range tolerations {
		if toleration.Key == "node.kubernetes.io/unreachable" || toleration.Key == "node.kubernetes.io/not-ready" {
			continue
		}
		filtered = append(filtered, toleration)
	}
	return filtered
}

func normalizeTolerations(tolerations []corev1.Toleration) []corev1.Toleration {
	copied := make([]corev1.Toleration, len(tolerations))
	copy(copied, tolerations)

	sort.Slice(copied, func(i, j int) bool {
		return tolerationKey(copied[i]) < tolerationKey(copied[j])
	})

	return copied
}

func tolerationKey(t corev1.Toleration) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s",
		t.Key, t.Operator, t.Value, t.Effect, tolerationSecondsString(t.TolerationSeconds))
}

func tolerationSecondsString(tolerationSeconds *int64) string {
	if tolerationSeconds != nil {
		return fmt.Sprintf("%d", *tolerationSeconds)
	}
	return "nil"
}
