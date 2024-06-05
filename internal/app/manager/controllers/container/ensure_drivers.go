package container

import (
	"context"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/app/manager/services"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (state *ContainerState) EnsureDrivers(client client.Client, wekaService services.WekaService) lifecycle.StepFunc {
	return func(ctx context.Context) error {
		ctx, _, end := instrumentation.GetLogSpan(ctx, "EnsureDrivers")
		defer end()

		container := state.Subject
		if container == nil {
			return &errors.ArgumentError{ArgName: "container", Message: "container is nil"}
		}

		if container.IsServiceContainer() {
			return nil
		}

		pod := state.Pod
		if pod == nil {
			return &errors.ArgumentError{ArgName: "pod", Message: "pod is nil"}
		}

		err := wekaService.ReconcileDriversStatus(ctx, pod)
		if err != nil {
			return &errors.RetryableError{Err: err, RetryAfter: 3 * time.Second}
		}
		meta.SetStatusCondition(&container.Status.Conditions, metav1.Condition{
			Type:   condition.CondEnsureDrivers,
			Status: metav1.ConditionTrue, Reason: "Success", Message: "Drivers are ensured",
		})
		if err = client.Status().Update(ctx, container); err != nil {
			return &ConditionUpdateError{
				WrappedError: errors.WrappedError{Err: err},
				Container:    container,
				Condition:    condition.CondEnsureDrivers,
			}
		}
		return nil
	}
}
