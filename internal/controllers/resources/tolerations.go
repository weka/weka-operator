package resources

import (
	corev1 "k8s.io/api/core/v1"
)

func ExpandNoScheduleTolerations(tolerations []corev1.Toleration) []corev1.Toleration {
	noScheduleToleration := corev1.Toleration{
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	}
	preferNoScheduleToleration := corev1.Toleration{
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectPreferNoSchedule,
	}
	tolerations = append(tolerations, noScheduleToleration)
	tolerations = append(tolerations, preferNoScheduleToleration)

	return tolerations
}

func ConditionalExpandNoScheduleTolerations(tolerations []corev1.Toleration, condition bool) []corev1.Toleration {
	if condition {
		return ExpandNoScheduleTolerations(tolerations)
	}
	return tolerations
}
