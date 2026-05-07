package validation

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clusterDataservicesFeCores rejects WekaClusters where
// dataServicesContainers > 0 and dataServicesFeCores is not 0.
// Data services containers must not have frontend cores assigned.
type clusterDataservicesFeCores struct{}

func (clusterDataservicesFeCores) ID() string {
	return "cluster_dataservices_fe_cores"
}

func (clusterDataservicesFeCores) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	if cluster.Spec.Dynamic == nil {
		return nil
	}

	d := cluster.Spec.Dynamic
	if d.DataServicesContainers <= 0 {
		return nil
	}

	feCores := d.GetDataServicesFeCores()

	if feCores == 0 {
		return nil
	}

	detail := fmt.Sprintf(
		"spec.dynamic.dataServicesFeCores (%d) must be 0 when dataServicesContainers (%d) is greater than 0. "+
			"Data services containers must not have frontend cores assigned.",
		feCores, d.DataServicesContainers,
	)
	return field.ErrorList{
		field.Invalid(
			field.NewPath("spec", "dynamic", "dataServicesFeCores"),
			feCores,
			detail,
		),
	}
}
