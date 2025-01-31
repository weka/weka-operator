package util

import (
	corev1 "k8s.io/api/core/v1"
)

// Check if the given taints can be tolerated by the given tolerations.
func CheckTolerations(taints []corev1.Taint, tolerations []corev1.Toleration) bool {
	for _, taint := range taints {
		tolerated := false
		for _, tol := range tolerations {
			if tol.Key == taint.Key && (tol.Operator == corev1.TolerationOpExists || tol.Value == taint.Value) {
				tolerated = true
				break
			}
		}
		if !tolerated {
			return false
		}
	}
	return true
}
