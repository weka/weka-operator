// package wekacluster contains the reconciliation logic for WekaCluster resources
package wekacluster

import (
	"context"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"go.opentelemetry.io/otel/codes"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// GetCredentialSteps returns the steps for managing credentials in the WekaCluster
func GetCredentialSteps(loop *wekaClusterReconcilerLoop) []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.GroupedSteps{
			Name: "ApplyClusterCredentialsSteps",
			Predicates: []lifecycle.PredicateFunc{
				lifecycle.IsNotFunc(loop.cluster.IsExpand),
			},
			Steps: []lifecycle.Step{
				&lifecycle.SingleStep{
					Condition: condition.CondClusterSecretsApplied,
					Run:       loop.ApplyCredentials,
				},
				&lifecycle.SingleStep{
					Condition: condition.CondAdminUserDeleted,
					Run:       loop.DeleteAdminUser,
					Predicates: []lifecycle.PredicateFunc{
						func() bool {
							for _, c := range loop.cluster.Status.Conditions {
								if c.Type == condition.CondClusterSecretsApplied {
									if time.Since(c.LastTransitionTime.Time) > time.Minute*5 {
										return true
									}
								}
							}
							return false
						},
					},
				},
			},
		},
		&lifecycle.SingleStep{
			Condition: condition.CondClusterClientSecretsCreated,
			Run:       loop.ensureClientLoginCredentials,
		},
		&lifecycle.SingleStep{
			Condition: condition.CondClusterClientSecretsApplied,
			Run:       loop.applyClientLoginCredentials,
		},
		&lifecycle.SingleStep{
			Condition: condition.CondClusterCsiSecretsCreated,
			Run:       loop.EnsureCsiLoginCredentials,
		},
		&lifecycle.SingleStep{
			Run: loop.EnsureCsiLoginCredentials,
			Throttling: &throttling.ThrottlingSettings{
				Interval: config.Consts.CsiLoginCredentialsUpdateInterval,
			},
		},
		&lifecycle.SingleStep{
			Condition: condition.CondClusterCsiSecretsApplied,
			Run:       loop.applyCsiLoginCredentials,
		},
	}
}

// GetPostClusterSteps returns the post-cluster configuration steps
func GetPostClusterSteps(loop *wekaClusterReconcilerLoop) []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SingleStep{
			Predicates: []lifecycle.PredicateFunc{
				lifecycle.IsNotFunc(loop.cluster.IsExpand),
			},
			Condition: condition.CondDefaultFsCreated,
			Run:       loop.EnsureDefaultFS,
		},
		&lifecycle.SingleStep{
			Predicates: []lifecycle.PredicateFunc{
				loop.ShouldDestroyS3Cluster,
			},
			Run: loop.DestroyS3Cluster,
		},
		&lifecycle.SingleStep{
			Name: "EnsureS3Cluster",
			Predicates: []lifecycle.PredicateFunc{
				loop.HasS3Containers,
			},
			Condition:       condition.CondS3ClusterCreated,
			Run:             loop.EnsureS3Cluster,
			ContinueOnError: true,
		},
		&lifecycle.SingleStep{
			Predicates: []lifecycle.PredicateFunc{
				lifecycle.IsNotFunc(loop.cluster.IsExpand),
				loop.HasNfsContainers,
			},
			Condition:       condition.ConfNfsConfigured,
			Run:             loop.EnsureNfs,
			ContinueOnError: true,
		},
		&lifecycle.SingleStep{
			Condition: condition.WekaHomeConfigured,
			Run:       loop.configureWekaHome,
		},
		&lifecycle.SingleStep{
			Condition: condition.CondClusterReady,
			Run:       loop.MarkAsReady,
		},
		&lifecycle.SingleStep{
			Run: loop.handleUpgrade,
		},
	}
}

func (r *wekaClusterReconcilerLoop) DeleteAdminUser(ctx context.Context) error {
	container := discovery.SelectActiveContainer(r.containers)
	if container == nil {
		return errors.New("No active container found")
	}
	wekaService := services.NewWekaService(r.ExecService, container)
	return wekaService.EnsureNoUser(ctx, "admin")
}

func (r *wekaClusterReconcilerLoop) configureWekaHome(ctx context.Context) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "configureWekaHome")
	defer end()

	wekaCluster := r.cluster
	containers := r.containers

	config, err := domain.GetWekahomeConfig(wekaCluster)
	if err != nil {
		return err
	}

	if config.Endpoint == "" {
		return nil // explicitly asked not to configure
	}

	driveContainer, err := discovery.SelectActiveContainerWithRole(ctx, containers, weka.WekaContainerModeDrive)
	if err != nil {
		return err
	}

	wekaService := services.NewWekaService(r.ExecService, driveContainer)
	err = wekaService.SetWekaHome(ctx, config)
	if err != nil {
		return err
	}

	return wekaService.EmitCustomEvent(ctx, "Weka cluster provisioned successfully")
}

func (r *wekaClusterReconcilerLoop) EnsureDefaultFS(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := discovery.SelectActiveContainer(r.containers)
	if container == nil {
		return errors.New("No active container found")
	}

	wekaService := services.NewWekaService(r.ExecService, container)
	status, err := wekaService.GetWekaStatus(ctx)
	if err != nil {
		return err
	}

	err = wekaService.CreateFilesystemGroup(ctx, "default")
	if err != nil {
		var fsGroupExists *services.FilesystemGroupExists
		if !errors.As(err, &fsGroupExists) {
			logger.Error(err, "DID NOT detect FS as already exists")
			return err
		}
	}

	// This defaults are not meant to be configurable, as instead weka should not require them.
	// Until then, user configuratino post cluster create

	var thinProvisionedLimitsConfigFS int64 = 100 * 1024 * 1024 * 1024 // half a total capacity allocated for thin provisioning
	thinProvisionedLimitsDefault := status.Capacity.TotalBytes / 10    // half a total capacity allocated for thin provisioning
	fsReservedCapacity := status.Capacity.TotalBytes / 100
	var configFsSize int64 = 10 * 1024 * 1024 * 1024
	var defaultFsSize int64 = 1 * 1024 * 1024 * 1024

	if defaultFsSize > thinProvisionedLimitsDefault {
		thinProvisionedLimitsDefault = defaultFsSize
	}

	err = wekaService.CreateFilesystem(ctx, ".config_fs", "default", services.FSParams{
		TotalCapacity:             strconv.FormatInt(thinProvisionedLimitsConfigFS, 10),
		ThickProvisioningCapacity: strconv.FormatInt(configFsSize, 10),
		ThinProvisioningEnabled:   true,
	})
	if err != nil {
		var fsExists *services.FilesystemExists
		if !errors.As(err, &fsExists) {
			return err
		}
	}

	err = wekaService.CreateFilesystem(ctx, "default", "default", services.FSParams{
		TotalCapacity:             strconv.FormatInt(thinProvisionedLimitsDefault, 10),
		ThickProvisioningCapacity: strconv.FormatInt(fsReservedCapacity, 10),
		ThinProvisioningEnabled:   true,
	})
	if err != nil {
		var fsExists *services.FilesystemExists
		if !errors.As(err, &fsExists) {
			return err
		}
	}

	logger.SetStatus(codes.Ok, "default filesystem ensured")

	return nil
}

func (r *wekaClusterReconcilerLoop) MarkAsReady(ctx context.Context) error {
	wekaCluster := r.cluster

	if wekaCluster.Status.Status != weka.WekaClusterStatusReady {
		wekaCluster.Status.Status = weka.WekaClusterStatusReady
		wekaCluster.Status.TraceId = ""
		wekaCluster.Status.SpanID = ""
		return r.getClient().Status().Update(ctx, wekaCluster)
	}

	return nil
}
