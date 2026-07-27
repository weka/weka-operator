package controllers

import (
	"reflect"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestOwnerDetailsFromPolicy(t *testing.T) {
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
	policy := &weka.WekaPolicy{
		ObjectMeta: metav1.ObjectMeta{Labels: labels},
		Spec: weka.WekaPolicySpec{
			Image:              &image,
			ImagePullSecret:    &imagePullSecret,
			Tolerations:        tolerations,
			ServiceAccountName: "weka-runtime",
		},
	}

	details := ownerDetailsFromPolicy(policy)

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
	if details.ServiceAccountName != policy.Spec.ServiceAccountName {
		t.Errorf("ServiceAccountName = %q, want %q", details.ServiceAccountName, policy.Spec.ServiceAccountName)
	}

	t.Run("empty optional fields retain default behavior", func(t *testing.T) {
		emptyDetails := ownerDetailsFromPolicy(&weka.WekaPolicy{})
		if emptyDetails.Image != "" {
			t.Errorf("Image = %q, want empty", emptyDetails.Image)
		}
		if emptyDetails.ImagePullSecret != "" {
			t.Errorf("ImagePullSecret = %q, want empty", emptyDetails.ImagePullSecret)
		}
		if emptyDetails.ServiceAccountName != "" {
			t.Errorf("ServiceAccountName = %q, want empty", emptyDetails.ServiceAccountName)
		}
	})
}
