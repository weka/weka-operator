package util

import (
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

func GetLastGuidPart(uid types.UID) string {
	guidLastPart := string(uid[strings.LastIndex(string(uid), "-")+1:])
	return guidLastPart
}

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
