package controllers

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestOwnerDetailsFrom(t *testing.T) {
	image := "quay.io/weka.io/weka-in-container:4.5.0"
	imagePullSecret := "weka-registry"
	tolerations := []corev1.Toleration{
		{
			Key:      "dedicated",
			Operator: corev1.TolerationOpEqual,
			Value:    "weka",
			Effect:   corev1.TaintEffectNoSchedule,
		},
	}
	labels := map[string]string{"app": "ensure-nics"}

	details := ownerDetailsFrom(ownerDetailsInput{
		Image:              &image,
		ImagePullSecret:    &imagePullSecret,
		Tolerations:        tolerations,
		Labels:             labels,
		ServiceAccountName: "weka-runtime",
	})

	if details.Image != image {
		t.Errorf("Image = %q, want %q", details.Image, image)
	}
	if details.ImagePullSecret != imagePullSecret {
		t.Errorf("ImagePullSecret = %q, want %q", details.ImagePullSecret, imagePullSecret)
	}
	if !reflect.DeepEqual(details.Tolerations, tolerations) {
		t.Errorf("Tolerations = %#v, want %#v", details.Tolerations, tolerations)
	}
	if !reflect.DeepEqual(details.Labels, labels) {
		t.Errorf("Labels = %#v, want %#v", details.Labels, labels)
	}
	if details.ServiceAccountName != "weka-runtime" {
		t.Errorf("ServiceAccountName = %q, want %q", details.ServiceAccountName, "weka-runtime")
	}
}

func TestOwnerDetailsFrom_Empty(t *testing.T) {
	details := ownerDetailsFrom(ownerDetailsInput{})
	if details.Image != "" {
		t.Errorf("Image = %q, want empty", details.Image)
	}
	if details.ImagePullSecret != "" {
		t.Errorf("ImagePullSecret = %q, want empty", details.ImagePullSecret)
	}
	if details.ServiceAccountName != "" {
		t.Errorf("ServiceAccountName = %q, want empty", details.ServiceAccountName)
	}
	if details.Tolerations != nil {
		t.Errorf("Tolerations = %#v, want nil", details.Tolerations)
	}
	if details.Labels != nil {
		t.Errorf("Labels = %#v, want nil", details.Labels)
	}
}
