package validation

import (
	"context"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clusterExtraVolumes validates spec.podConfig.extraVolumes and
// spec.podConfig.extraVolumeMounts. See validateExtraVolumes for the rule body,
// shared with clientExtraVolumes.
type clusterExtraVolumes struct{}

func (clusterExtraVolumes) ID() string {
	return "cluster_extra_volumes"
}

func (clusterExtraVolumes) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	if cluster.Spec.PodConfig == nil {
		return nil
	}
	return validateExtraVolumes(
		cluster.Spec.PodConfig.ExtraVolumes,
		cluster.Spec.PodConfig.ExtraVolumeMounts,
		field.NewPath("spec", "podConfig"),
	)
}
