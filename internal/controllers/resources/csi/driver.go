package csi

import (
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
)

func NewCSIDriver(name string) *storagev1.CSIDriver {
	fsGroupPolicy := storagev1.FileFSGroupPolicy
	return &storagev1.CSIDriver{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: storagev1.CSIDriverSpec{
			AttachRequired: pointer.Bool(true),
			PodInfoOnMount: pointer.Bool(true),
			VolumeLifecycleModes: []storagev1.VolumeLifecycleMode{
				storagev1.VolumeLifecyclePersistent,
			},
			FSGroupPolicy: &fsGroupPolicy,
		},
	}
}
