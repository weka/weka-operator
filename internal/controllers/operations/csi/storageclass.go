package csi

import (
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func NewCsiStorageClass(secretName string, driverName string, storageClassName string, fileSystemName string, mountOptions ...string) *storagev1.StorageClass {
	storageClass := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: storageClassName,
		},
		Provisioner: driverName,
		Parameters: map[string]string{
			"capacityEnforcement":                                    "HARD",
			"csi.storage.k8s.io/controller-expand-secret-name":       secretName,
			"csi.storage.k8s.io/controller-expand-secret-namespace":  "default",
			"csi.storage.k8s.io/controller-publish-secret-name":      secretName,
			"csi.storage.k8s.io/controller-publish-secret-namespace": "default",
			"csi.storage.k8s.io/node-publish-secret-name":            secretName,
			"csi.storage.k8s.io/node-publish-secret-namespace":       "default",
			"csi.storage.k8s.io/node-stage-secret-name":              secretName,
			"csi.storage.k8s.io/node-stage-secret-namespace":         "default",
			"csi.storage.k8s.io/provisioner-secret-name":             secretName,
			"csi.storage.k8s.io/provisioner-secret-namespace":        "default",
			"filesystemName": fileSystemName,
			"volumeType":     "dir/v1",
		},
		MountOptions:         mountOptions,
		ReclaimPolicy:        func() *corev1.PersistentVolumeReclaimPolicy { p := corev1.PersistentVolumeReclaimDelete; return &p }(),
		AllowVolumeExpansion: func() *bool { b := true; return &b }(),
		VolumeBindingMode:    func() *storagev1.VolumeBindingMode { m := storagev1.VolumeBindingImmediate; return &m }(),
	}

	return storageClass
}
