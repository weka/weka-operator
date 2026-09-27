package resources

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// HelperContainerResources sizes auxiliary containers that copy a file, pull an image or idle
// waiting to be observed. Every container needs cpu and memory limits or an admission policy
// enforcing them rejects the whole pod, and these do too little work to be worth a knob each.
// Values match the csi-wekafs sidecar profile so helper containers size consistently.
func HelperContainerResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4m"),
			corev1.ResourceMemory: resource.MustParse("48Mi"),
		},
	}
}

// HelperContainerResourcesID is a stable identifier for the values above, for callers that hash a
// workload spec to decide whether to redeploy. It is derived rather than written out so it cannot
// drift; util.HashStruct cannot consume ResourceRequirements itself, as it rejects the maps in
// ResourceList.
func HelperContainerResourcesID() string {
	r := HelperContainerResources()
	return fmt.Sprintf("limits(cpu=%s,memory=%s)/requests(cpu=%s,memory=%s)",
		r.Limits.Cpu(), r.Limits.Memory(), r.Requests.Cpu(), r.Requests.Memory())
}
