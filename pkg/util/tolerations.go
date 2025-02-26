package util

import (
	corev1 "k8s.io/api/core/v1"
)

// Check if the given taints can be tolerated by the given tolerations.
func CheckTolerations(taints []corev1.Taint, tolerations []corev1.Toleration) bool {
TAINT:
	for _, taint := range taints {
		for _, toleration := range tolerations {
			if toleration.ToleratesTaint(&taint) {
				continue TAINT
			}
		}
		return false
	}
	return true
}
