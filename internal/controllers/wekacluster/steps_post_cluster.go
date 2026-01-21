// package wekacluster contains the reconciliation logic for WekaCluster resources
package wekacluster

import (
	"context"
	"fmt"
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
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// GetCredentialSteps returns the steps for managing credentials in the WekaCluster
func GetCredentialSteps(loop *wekaClusterReconcilerLoop) []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.GroupedSteps{
			Name: "ApplyClusterCredentialsSteps",
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(loop.cluster.IsExpand),
			},
			Steps: []lifecycle.Step{
				&lifecycle.SimpleStep{
					State: &lifecycle.State{
						Name: condition.CondClusterSecretsApplied,
					},
					Run: loop.ApplyCredentials,
				},
				&lifecycle.SimpleStep{
					State: &lifecycle.State{
						Name: condition.CondAdminUserDeleted,
					},
					Run: loop.DeleteAdminUser,
					Predicates: lifecycle.Predicates{
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
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name: condition.CondClusterClientSecretsCreated,
			},
			Run: loop.ensureClientLoginCredentials,
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name: condition.CondClusterClientSecretsApplied,
			},
			Run: loop.applyClientLoginCredentials,
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name: condition.CondClusterCsiSecretsCreated,
			},
			Run: loop.EnsureCsiLoginCredentials,
		},
		&lifecycle.SimpleStep{
			Run: loop.EnsureCsiLoginCredentials,
			Throttling: &throttling.ThrottlingSettings{
				Interval:          config.Consts.CsiLoginCredentialsUpdateInterval,
				EnsureStepSuccess: true,
			},
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name: condition.CondClusterCsiSecretsApplied,
			},
			Run: loop.applyCsiLoginCredentials,
		},
	}
}

// GetPostClusterSteps returns the post-cluster configuration steps
func GetPostClusterSteps(loop *wekaClusterReconcilerLoop) []lifecycle.Step {
	return []lifecycle.Step{
		&lifecycle.SimpleStep{
			Run: loop.ConfigureKms,
			State: &lifecycle.State{
				Name: condition.CondClusterKMSConfigured,
			},
			Predicates: lifecycle.Predicates{
				loop.ShouldConfigureKms,
			},
		},
		&lifecycle.SimpleStep{
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(loop.cluster.IsExpand),
			},
			State: &lifecycle.State{
				Name: condition.CondDefaultFsCreated,
			},
			Run: loop.EnsureDefaultFS,
		},
		&lifecycle.SimpleStep{
			Predicates: lifecycle.Predicates{
				loop.ShouldDestroyS3Cluster,
			},
			Run: loop.DestroyS3Cluster,
		},
		&lifecycle.SimpleStep{
			Predicates: lifecycle.Predicates{
				loop.HasS3Containers,
			},
			State: &lifecycle.State{
				Name: condition.CondS3ClusterCreated,
			},
			Run: loop.EnsureS3Cluster,
		},
		&lifecycle.SimpleStep{
			Predicates: lifecycle.Predicates{
				lifecycle.IsNotFunc(loop.cluster.IsExpand),
				loop.HasNfsContainers,
			},
			State: &lifecycle.State{
				Name: condition.ConfNfsConfigured,
			},
			Run: loop.EnsureNfs,
		},
		&lifecycle.SimpleStep{
			Predicates: lifecycle.Predicates{
				loop.HasNfsContainers,
				loop.ShouldConfigureNfsIpRanges,
			},
			ContinueOnError: true,
			Run:             loop.EnsureNfsIpRanges,
		},
		&lifecycle.SimpleStep{
			Predicates: lifecycle.Predicates{
				loop.HasDataServicesContainers,
			},
			State: &lifecycle.State{
				Name: condition.CondDataServicesConfigured,
			},
			Run: loop.EnsureDataServicesGlobalConfig,
		},
		&lifecycle.SimpleStep{
			Predicates: lifecycle.Predicates{
				loop.ShouldDestroySmbwCluster,
			},
			Run: loop.DestroySmbwCluster,
		},
		&lifecycle.SimpleStep{
			Predicates: lifecycle.Predicates{
				loop.HasSmbwContainers,
			},
			State: &lifecycle.State{
				Name: condition.CondSmbwClusterCreated,
			},
			Run: loop.EnsureSmbwCluster,
		},
		&lifecycle.SimpleStep{
			Predicates: lifecycle.Predicates{
				loop.HasSmbwContainers,
				loop.ShouldJoinSmbwDomain,
			},
			State: &lifecycle.State{
				Name: condition.CondSmbwDomainJoined,
			},
			Run: loop.JoinSmbwDomain,
		},
		&lifecycle.SimpleStep{
			Predicates: lifecycle.Predicates{
				loop.HasSmbwContainers,
				loop.ShouldConfigureSmbwIpRanges,
			},
			Run: loop.EnsureSmbwIpRanges,
		},
		&lifecycle.SimpleStep{
			ContinueOnError: true,
			Run:             loop.EnsureTelemetry,
			Throttling: &throttling.ThrottlingSettings{
				Interval:          config.Consts.TelemetryUpdateInterval,
				EnsureStepSuccess: true,
			},
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name: condition.WekaHomeConfigured,
			},
			Run:             loop.configureWekaHome,
			ContinueOnError: true,
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name: condition.CondManagementServiceConfigured,
			},
			Throttling: &throttling.ThrottlingSettings{
				Interval:          config.Consts.ManagementServiceUpdateInterval,
				EnsureStepSuccess: true,
			},
			SkipStepStateCheck: true,
			Run:                loop.EnsureManagementService,
			ContinueOnError:    true,
		},
		&lifecycle.SimpleStep{
			State: &lifecycle.State{
				Name: condition.CondClusterReady,
			},
			Run: loop.MarkAsReady,
		},
		&lifecycle.SimpleStep{
			Run: loop.handleUpgrade,
		},
	}
}

func (r *wekaClusterReconcilerLoop) ConfigureKms(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	activeContainer, err := discovery.SelectActiveContainerWithRole(ctx, r.containers, weka.WekaContainerModeDrive)
	if err != nil {
		return err
	}

	executor, err := r.ExecService.GetExecutor(ctx, activeContainer)
	if err != nil {
		return err
	}

	cmd := fmt.Sprintf("weka security kms set vault '%s' '%s' --kubernetes-role '%s' --auth-path '%s' --transit-path '%s'",
		r.cluster.Spec.Encryption.VaultConfig.Address,
		r.cluster.Spec.Encryption.VaultConfig.KeyName,
		r.cluster.Spec.Encryption.VaultConfig.Role,
		r.cluster.Spec.Encryption.VaultConfig.AuthPath,
		r.cluster.Spec.Encryption.VaultConfig.TransitPath,
	)
	stdout, stderr, err := executor.ExecNamed(ctx, "ConfigureKms", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "Failed to configure KMS: %s\n%s", stderr.String(), stdout.String())
	}

	logger.Info("KMS configured successfully")

	return nil
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
	ctx, _, end := instrumentation.GetLogSpan(ctx, "")
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

	return wekaService.EmitCustomEvent(ctx, "Weka cluster provisioned successfully", utils.GetKubernetesVersion(r.Manager))
}

func (r *wekaClusterReconcilerLoop) ShouldConfigureKms() bool {
	if r.cluster.Spec.Encryption == nil || r.cluster.Spec.Encryption.VaultConfig == nil {
		return false
	}
	if r.cluster.Spec.Encryption.VaultConfig.Method != "kubernetes" {
		return false
	}
	return true
}

func (r *wekaClusterReconcilerLoop) ShouldEncryptFs() bool {
	if r.ShouldConfigureKms() {
		return true
	}
	if r.IsInternalEncryptionEnabled() {
		return true
	}
	return false
}

func (r *wekaClusterReconcilerLoop) IsInternalEncryptionEnabled() bool {
	if r.cluster.Spec.Encryption != nil && r.cluster.Spec.Encryption.InternalConfig != nil && r.cluster.Spec.Encryption.InternalConfig.Enabled {
		return true
	}
	return false
}

func (r *wekaClusterReconcilerLoop) EnsureDefaultFS(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := discovery.SelectActiveContainer(r.containers)
	if container == nil {
		return errors.New("No active container found")
	}

	timeout := time.Second * 30
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, container, &timeout)
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

	isEncrypted := r.ShouldEncryptFs()

	err = wekaService.CreateFilesystem(ctx, ".config_fs", "default", services.FSParams{
		TotalCapacity:             strconv.FormatInt(thinProvisionedLimitsConfigFS, 10),
		ThickProvisioningCapacity: strconv.FormatInt(configFsSize, 10),
		ThinProvisioningEnabled:   true,
		IsEncrypted:               isEncrypted,
		NoKmsEncryption:           r.IsInternalEncryptionEnabled(),
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
		IsEncrypted:               isEncrypted,
		NoKmsEncryption:           r.IsInternalEncryptionEnabled(),
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
