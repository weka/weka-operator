package controllers

import (
	"context"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
)

func IsUnhealthy(ctx context.Context, container *weka.WekaContainer) (bool, string, error) {
	// if we cannot  tell - it is false, "", error
	// if we can reliably tell - it is true, "reason", nil
	// there should not be a case where we can tell it is unhealthy and cannot explain based on what, or to have a error to return
	if container.IsMarkedForDeletion() {
		return true, "Marked for deletion", nil
	}
	return false, "", nil // we do not have enough data to tell that it is unhealthy
}
