package controllers

import (
	"context"
	"github.com/pkg/errors"
	"github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/lifecycle"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type UpgradeController struct {
	Containers  []*v1alpha1.WekaContainer
	TargetImage string
	Client      client.Client
}

func NewUpgradeController(client client.Client, containers []*v1alpha1.WekaContainer, targetImage string) *UpgradeController {
	return &UpgradeController{
		Containers:  containers,
		TargetImage: targetImage,
		Client:      client,
	}
}

func (u *UpgradeController) UpdateContainer(ctx context.Context, container *v1alpha1.WekaContainer) error {
	if container.Status.LastAppliedImage != u.TargetImage {
		if container.Spec.Image != u.TargetImage {
			container.Spec.Image = u.TargetImage
			if err := u.Client.Update(ctx, container); err != nil {
				return err
			}
		}
	}
	return nil
}

func (u *UpgradeController) AreUpgraded() bool {
	for _, container := range u.Containers {
		if container.Status.LastAppliedImage != u.TargetImage {
			return false
		}
	}
	return true
}

func (u *UpgradeController) AllAtOnceUpgrade(ctx context.Context) error {
	for _, container := range u.Containers {
		if err := u.UpdateContainer(ctx, container); err != nil {
			return err
		}
	}
	if !u.AreUpgraded() {
		return lifecycle.NewWaitError(errors.New("container upgrade not finished yet"))
	}
	return nil
}

func (u *UpgradeController) RollingUpgrade(ctx context.Context) error {
	for _, container := range u.Containers {
		if container.Status.LastAppliedImage != container.Spec.Image {
			return lifecycle.NewWaitError(errors.New("container upgrade not finished yet"))
		}
	}

	for _, container := range u.Containers {
		if container.Spec.Image != u.TargetImage {
			err := u.UpdateContainer(ctx, container)
			if err != nil {
				return err
			}
			return lifecycle.NewWaitError(errors.New("container upgrade not finished yet"))
		}
	}
	return nil
}
