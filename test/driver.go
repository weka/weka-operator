package test

import (
	"context"
	"testing"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type DriverBuilder struct {
	ClusterTest
}

func (d *DriverBuilder) Run(ctx context.Context) func(t *testing.T) {
	return func(t *testing.T) {
		t.Run("Deploy Driver Builder", d.DeployDriverBuilder(ctx))
	}
}

// DeployDriverBuilder deploys the driver builder
func (d *DriverBuilder) DeployDriverBuilder(ctx context.Context) func(t *testing.T) {
	return func(t *testing.T) {
		ctx, logger, done := instrumentation.GetLogSpan(ctx, "DeployDriverBuilder")
		defer done()

		if d.Image == "" {
			t.Fatalf("driver image not set")
		}

		container := &wekav1alpha1.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "weka-driver-builder",
				Namespace: d.Cluster.OperatorNamespace,
				Labels: map[string]string{
					"app": "weka-driver-builder",
				},
			},
			Spec: wekav1alpha1.WekaContainerSpec{
				AgentPort: 60001,
				NodeSelector: map[string]string{
					"weka.io/role": "builder",
				},
				Image:             d.Image,
				ImagePullSecret:   "quay-io-robot-secret",
				Mode:              "dist",
				WekaContainerName: "dist",
				NumCores:          1,
				Port:              60002,
			},
		}
		if err := d.Create(ctx, container); err != nil {
			if apierrors.IsAlreadyExists(err) {
				logger.Info("driver builder container already exists")
			} else {
				t.Fatalf("failed to create driver builder container: %v", err)
			}
		}

		service := &v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "weka-driver-builder",
				Namespace: d.Cluster.OperatorNamespace,
			},
			Spec: v1.ServiceSpec{
				Type: v1.ServiceTypeClusterIP,
				Selector: map[string]string{
					"app": "weka-driver-builder",
				},
				Ports: []v1.ServicePort{
					{
						Name:       "weka-driver-builder",
						Port:       60002,
						TargetPort: intstr.FromInt(60002),
					},
				},
			},
		}
		if err := d.Create(ctx, service); err != nil {
			if apierrors.IsAlreadyExists(err) {
				logger.Info("driver builder service already exists")
			} else {
				t.Fatalf("failed to create driver builder service: %v", err)
			}
		}
	}
}
