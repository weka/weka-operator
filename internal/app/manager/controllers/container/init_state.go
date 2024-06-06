package container

import (
	"context"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (state *ContainerState) InitState(client client.Client) lifecycle.StepFunc {
	return func(ctx context.Context) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "InitState")
		defer end()

		container := state.Subject
		if container == nil {
			return &lifecycle.StateError{
				Property: "Subject",
				Message:  "container is nil",
			}
		}

		if container.Status.Conditions == nil {
			container.Status.Conditions = []metav1.Condition{}
		}

		// All container types are being set to False on init
		// This includes types not listed here (beyond dist and drivers-loader)
		// TODO: Is this expected?
		changes := false
		if !container.DriversReady() && container.SupportsEnsureDriversCondition() {
			changes = true
			meta.SetStatusCondition(&container.Status.Conditions,
				metav1.Condition{Type: condition.CondEnsureDrivers, Status: metav1.ConditionFalse, Message: "Init", Reason: "Init"},
			)
		}

		if container.Status.LastAppliedImage == "" {
			container.Status.LastAppliedImage = container.Spec.Image
			changes = true
		}

		if changes {
			err := client.Status().Update(ctx, container)
			if err != nil {
				return err
			}
		}

		logger.SetPhase("INIT_STATE")
		return nil
	}
}
