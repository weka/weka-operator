package csi

import (
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer" //nolint:staticcheck // using deprecated API, will be updated separately
)

func NewCsiDriver(name string, fsGroupPolicy storagev1.FSGroupPolicy) *storagev1.CSIDriver {
	return &storagev1.CSIDriver{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: GetCsiLabels(name, CSIDriver, nil, nil),
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
