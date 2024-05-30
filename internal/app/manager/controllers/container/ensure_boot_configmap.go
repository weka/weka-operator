package container

import (
	"context"

	"github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle"
	"github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/util"

	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (state *ContainerState) EnsureBootConfigMap(c client.Client, bootScriptConfigName string) lifecycle.StepFunc {
	return func(ctx context.Context) error {
		ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureBootConfigMap")
		defer end()

		container := state.Subject
		if container == nil {
			return &errors.ArgumentError{ArgName: "container", Message: "container is nil"}
		}

		bundledConfigMap := &v1.ConfigMap{}
		podNamespace, err := util.GetPodNamespace()
		if err != nil {
			logger.Error(err, "Error getting pod namespace")
			return err
		}
		key := client.ObjectKey{Namespace: podNamespace, Name: bootScriptConfigName}
		if err := c.Get(ctx, key, bundledConfigMap); err != nil {
			if apierrors.IsNotFound(err) {
				logger.Error(err, "Bundled config map not found")
				return err
			}
			logger.Error(err, "Error getting bundled config map")
			return err
		}

		bootScripts := &v1.ConfigMap{}
		err = c.Get(ctx, client.ObjectKey{Namespace: container.Namespace, Name: bootScriptConfigName}, bootScripts)
		if err != nil {
			if apierrors.IsNotFound(err) {
				bootScripts.Namespace = container.Namespace
				bootScripts.Name = bootScriptConfigName
				bootScripts.Data = bundledConfigMap.Data
				if err := c.Create(ctx, bootScripts); err != nil {
					if apierrors.IsAlreadyExists(err) {
						logger.Info("Boot scripts config map already exists in designated namespace")
					} else {
						logger.Error(err, "Error creating boot scripts config map")
					}
				}
				logger.Info("Created boot scripts config map in designated namespace")
			}
		}

		if !util.IsEqualConfigMapData(bootScripts, bundledConfigMap) {
			bootScripts.Data = bundledConfigMap.Data
			if err := c.Update(ctx, bootScripts); err != nil {
				logger.Error(err, "Error updating boot scripts config map")
				return err
			}
			logger.InfoWithStatus(codes.Ok, "Updated and reconciled boot scripts config map in designated namespace")

		}
		logger.SetPhase("BOOT_CONFIG_MAP_EXISTS")
		return nil
	}
}
