package util

import (
	"slices"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
)

func GetLastGuidPart(uid types.UID) string {
	guidLastPart := string(uid[strings.LastIndex(string(uid), "-")+1:])
	return guidLastPart
}

func ExpandTolerations(tolerations []v1.Toleration, simpleTolerations []string, rawTolerations []v1.Toleration) []v1.Toleration {
	for _, toleration := range simpleTolerations {
		tolerations = append(tolerations,
			v1.Toleration{
				Key:      toleration,
				Operator: v1.TolerationOpExists,
				Effect:   v1.TaintEffectNoSchedule,
			},
			v1.Toleration{
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

func NormalizeTolerations(tolerations []v1.Toleration) []v1.Toleration {
	if tolerations == nil {
		return []v1.Toleration{}
	}
	return tolerations
}

// DedupeTolerations drops repeated tolerations, keeping the first of each, so a user's own
// `operator: Exists` next to the one clientsTolerateAll adds does not change the pod hash.
func DedupeTolerations(tolerations []v1.Toleration) []v1.Toleration {
	out := make([]v1.Toleration, 0, len(tolerations))
	for _, t := range tolerations {
		if !slices.ContainsFunc(out, func(o v1.Toleration) bool { return equality.Semantic.DeepEqual(o, t) }) {
			out = append(out, t)
		}
	}
	return out
}
