package resources

import (
	"fmt"
	"strings"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

type HugePagesDetails struct {
	HugePagesStr          string
	HugePagesK8sSuffix    string
	HugePagesMb           int
	WekaMemoryString      string
	HugePagesResourceName corev1.ResourceName
}

func (f *PodFactory) getHugePagesDetails() HugePagesDetails {
	hugePagesStr := ""
	hugePagesK8sSuffix := "2Mi"
	wekaMemoryString := ""
	if f.container.Spec.HugepagesSize == "1Gi" {
		hugePagesK8sSuffix = f.container.Spec.HugepagesSize
		hugePagesStr = fmt.Sprintf("%dGi", f.container.Spec.Hugepages/1000)
		wekaMemoryString = fmt.Sprintf("%dGiB", f.container.Spec.Hugepages/1000)
	} else {
		hugePagesStr = fmt.Sprintf("%dMi", f.container.Spec.Hugepages)
		hugePagesK8sSuffix = "2Mi"
		offset := f.getHugePagesOffset()
		wekaMemoryString = fmt.Sprintf("%dMiB", f.container.Spec.Hugepages-offset)
	}

	if f.container.Spec.HugepagesOverride != "" {
		wekaMemoryString = f.container.Spec.HugepagesOverride
	}

	hugePagesName := corev1.ResourceName(
		strings.Join(
			[]string{corev1.ResourceHugePagesPrefix, hugePagesK8sSuffix},
			""))

	return HugePagesDetails{
		HugePagesStr:          hugePagesStr,
		HugePagesK8sSuffix:    hugePagesK8sSuffix,
		WekaMemoryString:      wekaMemoryString,
		HugePagesResourceName: hugePagesName,
		HugePagesMb:           f.container.Spec.Hugepages,
	}
}

func (f *PodFactory) getHugePagesOffset() int {
	offset := f.container.Spec.HugepagesOffset
	// get default if not set
	if offset == 0 {
		if f.container.Spec.Mode == weka.WekaContainerModeDrive {
			offset = 200 * f.container.Spec.NumDrives
		} else {
			offset = 200
		}
	}
	return offset
}

func (f *PodFactory) setHugePages(pod *corev1.Pod) {
	// Skip hugepages for drivers containers (they only load kernel modules, don't run Weka processes)
	if f.container.IsDriversContainer() {
		return
	}

	hgDetails := f.getHugePagesDetails()

	// Add hugepages volume
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: "hugepages",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{
				Medium: corev1.StorageMedium(fmt.Sprintf("HugePages-%s", hgDetails.HugePagesK8sSuffix)),
			},
		},
	})

	// Add hugepages volume mount
	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
		Name:      "hugepages",
		MountPath: "/dev/hugepages",
	})
}
