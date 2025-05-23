package wekacontainer

import (
	"slices"

	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/config"
)

func MetricsSteps(loop *containerReconcilerLoop) []lifecycle.Step {
	container := loop.container
	
	return []lifecycle.Step{
		&lifecycle.SingleStep{
			Name: "SetStatusMetrics",
			Run:  lifecycle.ForceNoError(loop.SetStatusMetrics),
			Predicates: lifecycle.Predicates{
				lifecycle.BoolValue(config.Config.Metrics.Containers.Enabled),
				lifecycle.IsNotFunc(loop.PodNotSet),
				func() bool {
					return slices.Contains(
						[]string{
							weka.WekaContainerModeCompute,
							weka.WekaContainerModeClient,
							weka.WekaContainerModeS3,
							weka.WekaContainerModeNfs,
							weka.WekaContainerModeDrive,
							// TODO: Expand to clients, introduce API-level(or not) HasManagement check
						}, container.Spec.Mode)
				},
			},
			Throttling: &throttling.ThrottlingSettings{
				Interval: config.Config.Metrics.Containers.PollingRate,
			},
		},
		&lifecycle.SingleStep{
			Name: "RegisterContainerOnMetrics",
			Run:  lifecycle.ForceNoError(loop.RegisterContainerOnMetrics),
			Predicates: lifecycle.Predicates{
				lifecycle.BoolValue(config.Config.Metrics.Containers.Enabled),
				func() bool {
					return slices.Contains(
						[]string{
							weka.WekaContainerModeCompute,
							weka.WekaContainerModeClient,
							weka.WekaContainerModeS3,
							weka.WekaContainerModeNfs,
							weka.WekaContainerModeDrive,
							weka.WekaContainerModeEnvoy,
						}, container.Spec.Mode)
				},
			},
			Throttling: &throttling.ThrottlingSettings{
				Interval: config.Config.Metrics.Containers.PollingRate,
			},
		},
		&lifecycle.SingleStep{
			Name: "ReportOtelMetrics",
			Run:  lifecycle.ForceNoError(loop.ReportOtelMetrics),
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(loop.PodNotSet),
			},
			Throttling: &throttling.ThrottlingSettings{
				Interval: config.Config.Metrics.Containers.PollingRate,
			},
		},
	}
}
