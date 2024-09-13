package condition

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Conditioner interface {
	SetCondition(condition metav1.Condition)
}

type (
	Patcher func(c Conditioner)
	Ready   struct{}
)

func (r *Ready) PatcherFailed(msg string) Patcher {
	return func(c Conditioner) {
		setReadyFailedWithMessage(c, msg)
	}
}

func setReadyFailedWithMessage(c Conditioner, message string) {
	c.SetCondition(metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  "Failed",
		Message: message,
	})
}
