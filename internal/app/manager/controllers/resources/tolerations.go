package resources

import v1 "k8s.io/api/core/v1"

func ExpandTolerations(tolerations []v1.Toleration, simpleTolerations []string, rawTolerations []v1.Toleration) []v1.Toleration {
	for _, toleration := range simpleTolerations {
		tolerations = append(tolerations, v1.Toleration{
			Key:      toleration,
			Operator: v1.TolerationOpExists,
			Effect:   v1.TaintEffectNoSchedule,
		})
		tolerations = append(tolerations, v1.Toleration{
			Key:      toleration,
			Operator: v1.TolerationOpExists,
			Effect:   v1.TaintEffectNoExecute,
		})
	}

	if rawTolerations != nil {
		tolerations = append(tolerations, rawTolerations...)
	}
	return tolerations
}
