package wekacontainer

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

func (r *containerReconcilerLoop) IsS3ClusterFormed(ctx context.Context) (bool, error) {
	cluster, err := r.getCluster(ctx)
	if err != nil {
		return false, err
	}

	return meta.IsStatusConditionTrue(cluster.Status.Conditions, condition.CondS3ClusterCreated), nil
}

func (r *containerReconcilerLoop) JoinS3Cluster(ctx context.Context) error {
	isFormed, err := r.IsS3ClusterFormed(ctx)
	if err != nil {
		return fmt.Errorf("error checking if S3 cluster is formed: %w", err)
	}
	if !isFormed {
		return lifecycle.NewWaitError(fmt.Errorf("S3 cluster is not formed yet, waiting for it to be formed"))
	}

	wekaService := services.NewWekaService(r.ExecService, r.container)
	return wekaService.JoinS3Cluster(ctx, *r.container.Status.ClusterContainerID)
}

func (r *containerReconcilerLoop) deleteEnvoyIfNoS3Neighbor(ctx context.Context) error {
	if !r.container.IsEnvoy() {
		return nil // only envoy containers should be checked
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	nodeName := r.container.GetNodeAffinity()

	if nodeName != "" {
		ownerRefs := r.container.GetOwnerReferences()
		if len(ownerRefs) == 0 {
			return errors.New("no owner references found")
		} else if len(ownerRefs) > 1 {
			return errors.New("more than one owner reference found")
		}

		ownerUid := string(ownerRefs[0].UID)
		// Check if there are any S3 wekacontainera on the same node
		s3Containers, err := discovery.GetClusterContainersByClusterUID(ctx, r.Manager.GetClient(), ownerUid, r.container.Namespace, weka.WekaContainerModeS3)
		if err != nil {
			return err
		}

		foundS3Neighbor := false
		for _, s3Container := range s3Containers {
			if s3Container.GetNodeAffinity() == nodeName {
				foundS3Neighbor = true
				break
			}
		}
		if foundS3Neighbor {
			logger.Debug("Found S3 neighbor, not deleting envoy container")
			return nil
		}
	}

	noS3NeighborKey := "NoS3Neighbor"

	if r.container.Status.Timestamps == nil {
		r.container.Status.Timestamps = make(map[string]metav1.Time)
	}
	if since, ok := r.container.Status.Timestamps[noS3NeighborKey]; !ok {
		r.container.Status.Timestamps[noS3NeighborKey] = metav1.Time{Time: time.Now()}
		if err := r.Status().Update(ctx, r.container); err != nil {
			return err
		}

		return lifecycle.NewWaitErrorWithDuration(
			errors.New("Envoy container has no S3 neighbor, waiting for 5 minutes before deleting it"),
			time.Second*15,
		)
	} else if time.Since(since.Time) < config.Config.DeleteEnvoyWithoutS3NeighborTimeout {
		logger.Info("Envoy container has no S3 neighbor, but waiting for 5 minutes before deleting it",
			"waited", time.Since(since.Time).String(),
			"node", nodeName,
		)
		return nil
	}

	_ = r.RecordEvent(
		v1.EventTypeNormal,
		"EnvoyContainerWithoutS3Neighbor",
		"Envoy container has no S3 neighbor, deleting it",
	)

	if err := r.Client.Delete(ctx, r.container); err != nil {
		return errors.Wrap(err, "failed to delete envoy container")
	}

	// Clear the timestamp to avoid re-deleting the container on next reconcile
	delete(r.container.Status.Timestamps, noS3NeighborKey)
	if err := r.Status().Update(ctx, r.container); err != nil {
		return errors.Wrap(err, "failed to update container status after deleting envoy")
	}

	logger.Info("Envoy container deleted as it has no S3 neighbor")

	return nil
}

func (r *containerReconcilerLoop) deleteTelemetryIfNoComputeNeighbor(ctx context.Context) error {
	if !r.container.IsTelemetry() {
		return nil // only telemetry containers should be checked
	}

	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	nodeName := r.container.GetNodeAffinity()

	if nodeName != "" {
		ownerRefs := r.container.GetOwnerReferences()
		if len(ownerRefs) == 0 {
			return errors.New("no owner references found")
		} else if len(ownerRefs) > 1 {
			return errors.New("more than one owner reference found")
		}

		ownerUid := string(ownerRefs[0].UID)
		// Check if there are any compute wekacontainers on the same node
		computeContainers, err := discovery.GetClusterContainersByClusterUID(ctx, r.Manager.GetClient(), ownerUid, r.container.Namespace, weka.WekaContainerModeCompute)
		if err != nil {
			return err
		}

		foundComputeNeighbor := false
		for _, computeContainer := range computeContainers {
			if computeContainer.GetNodeAffinity() == nodeName {
				foundComputeNeighbor = true
				break
			}
		}
		if foundComputeNeighbor {
			logger.Debug("Found compute neighbor, not deleting telemetry container")
			return nil
		}
	}

	noComputeNeighborKey := "NoComputeNeighbor"

	if r.container.Status.Timestamps == nil {
		r.container.Status.Timestamps = make(map[string]metav1.Time)
	}
	if since, ok := r.container.Status.Timestamps[noComputeNeighborKey]; !ok {
		r.container.Status.Timestamps[noComputeNeighborKey] = metav1.Time{Time: time.Now()}
		if err := r.Status().Update(ctx, r.container); err != nil {
			return err
		}

		return lifecycle.NewWaitErrorWithDuration(
			errors.New("Telemetry container has no compute neighbor, waiting before deleting it"),
			time.Second*15,
		)
	} else if time.Since(since.Time) < config.Config.DeleteTelemetryWithoutComputeNeighborTimeout {
		logger.Info("Telemetry container has no compute neighbor, but waiting before deleting it",
			"waited", time.Since(since.Time).String(),
			"node", nodeName,
		)
		return nil
	}

	_ = r.RecordEvent(
		v1.EventTypeNormal,
		"TelemetryContainerWithoutComputeNeighbor",
		"Telemetry container has no compute neighbor, deleting it",
	)

	if err := r.Client.Delete(ctx, r.container); err != nil {
		return errors.Wrap(err, "failed to delete telemetry container")
	}

	// Clear the timestamp to avoid re-deleting the container on next reconcile
	delete(r.container.Status.Timestamps, noComputeNeighborKey)
	if err := r.Status().Update(ctx, r.container); err != nil {
		return errors.Wrap(err, "failed to update container status after deleting telemetry")
	}

	logger.Info("Telemetry container deleted as it has no compute neighbor")

	return nil
}
