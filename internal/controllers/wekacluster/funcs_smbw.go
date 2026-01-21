package wekacluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"go.opentelemetry.io/otel/codes"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

func (r *wekaClusterReconcilerLoop) EnsureSmbwCluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster

	// Check if SMB-W config is defined
	if cluster.Spec.SmbwConfig == nil {
		logger.Info("SMB-W config not defined, skipping cluster creation")
		return nil
	}

	execInContainer := discovery.SelectActiveContainer(r.containers)
	wekaService := services.NewWekaService(r.ExecService, execInContainer)

	// Check if SMB-W cluster already exists
	smbwCluster, err := wekaService.GetSmbwCluster(ctx)
	if err != nil {
		err = errors.Wrap(err, "Failed to get SMB-W cluster")
		return err
	}

	if smbwCluster.Active {
		logger.Info("SMB-W cluster already exists")
		return nil
	}

	smbwContainers := r.SelectSmbwContainers(r.containers)
	containerIds := []int{}
	for _, c := range smbwContainers {
		if len(containerIds) == config.Consts.FormSmbwClusterMaxContainerCount {
			logger.Debug("Max SMB-W containers reached for initial SMB-W cluster creation", "maxContainers", config.Consts.FormSmbwClusterMaxContainerCount)
			break
		}

		if c.Status.ClusterContainerID == nil {
			msg := fmt.Sprintf("SMB-W container %s does not have a cluster container id", c.Name)
			logger.Debug(msg)
			continue
		}
		containerIds = append(containerIds, *c.Status.ClusterContainerID)
	}

	if len(containerIds) < config.Consts.FormSmbwClusterMinContainerCount {
		err := fmt.Errorf("not enough ready SMB-W containers: have %d, need at least %d",
			len(containerIds), config.Consts.FormSmbwClusterMinContainerCount)
		logger.Debug(err.Error())
		return lifecycle.NewWaitError(err)
	}

	logger.Debug("Creating SMB-W cluster", "containers", containerIds)

	clusterName := cluster.Spec.SmbwConfig.ClusterName
	if clusterName == "" {
		clusterName = "default"
	}

	err = wekaService.CreateSmbwCluster(ctx, services.SmbwParams{
		ClusterName:  clusterName,
		DomainName:   cluster.Spec.SmbwConfig.DomainName,
		ContainerIds: containerIds,
	})
	if err != nil {
		var smbwClusterExists *services.SmbwClusterExists
		if !errors.As(err, &smbwClusterExists) {
			return err
		}
	}

	logger.SetStatus(codes.Ok, "SMB-W cluster ensured")

	return nil
}

func (r *wekaClusterReconcilerLoop) ShouldDestroySmbwCluster() bool {
	if !r.cluster.Spec.GetOverrides().AllowSmbwClusterDestroy {
		return false
	}

	// if spec contains desired SMB-W containers, do not destroy the cluster
	template, ok := allocator.GetTemplateByName(r.cluster.Spec.Template, *r.cluster)
	if !ok {
		return false
	}
	if template.SmbwContainers > 0 {
		return false
	}

	containers := r.SelectSmbwContainers(r.containers)

	// if there are more than 1 SMB-W container, we should not destroy the cluster
	if len(containers) > 1 {
		return false
	}

	// if SMB-W cluster was not created, we should not destroy it
	if !meta.IsStatusConditionTrue(r.cluster.Status.Conditions, condition.CondSmbwClusterCreated) {
		return false
	}

	return true
}

func (r *wekaClusterReconcilerLoop) DestroySmbwCluster(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := discovery.SelectActiveContainer(r.containers)

	wekaService := services.NewWekaService(r.ExecService, container)

	smbwContainerIds, err := wekaService.ListSmbwClusterContainers(ctx)
	if err != nil {
		return err
	}
	logger.Info("SMB-W cluster containers", "containers", smbwContainerIds)

	if len(smbwContainerIds) > 1 {
		err := fmt.Errorf("more than one container in SMB-W cluster: %v", smbwContainerIds)
		return lifecycle.NewWaitError(err)
	}

	logger.Info("Destroying SMB-W cluster")
	err = wekaService.DeleteSmbwCluster(ctx)
	if err != nil {
		err = errors.Wrap(err, "Failed to delete SMB-W cluster")
		return err
	}

	// invalidate SMB-W cluster created condition
	changed := meta.SetStatusCondition(&r.cluster.Status.Conditions, metav1.Condition{
		Type:   condition.CondSmbwClusterCreated,
		Status: metav1.ConditionFalse,
		Reason: "DestroySmbwCluster",
	})
	if changed {
		err := r.getClient().Status().Update(ctx, r.cluster)
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *wekaClusterReconcilerLoop) ShouldJoinSmbwDomain() bool {
	if r.cluster.Spec.SmbwConfig == nil {
		return false
	}

	// Domain join requires both username and secret
	if r.cluster.Spec.SmbwConfig.UserName == "" {
		return false
	}
	if r.cluster.Spec.SmbwConfig.DomainJoinSecret == "" {
		return false
	}

	return true
}

func (r *wekaClusterReconcilerLoop) JoinSmbwDomain(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	cluster := r.cluster

	// Check if SMB-W config is defined
	if cluster.Spec.SmbwConfig == nil {
		logger.Info("SMB-W config not defined, skipping domain join")
		return nil
	}

	// Get username from spec
	username := cluster.Spec.SmbwConfig.UserName
	if username == "" {
		logger.Info("SMB-W username not defined, skipping domain join")
		return nil
	}

	// Get password from secret
	secretName := cluster.Spec.SmbwConfig.DomainJoinSecret
	if secretName == "" {
		logger.Info("SMB-W domain join secret not defined, skipping domain join")
		return nil
	}

	password, err := r.getSmbwDomainJoinPassword(ctx, secretName)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to get SMB-W domain join password from secret %s: %v", secretName, err)
		r.Recorder.Event(r.cluster, "Warning", "SmbwDomainJoinFailed", errMsg)
		return errors.Wrap(err, "Failed to get SMB-W domain join password")
	}

	execInContainer := discovery.SelectActiveContainer(r.containers)
	if execInContainer == nil {
		err := errors.New("No active container found for SMB-W domain join")
		r.Recorder.Event(r.cluster, "Warning", "SmbwDomainJoinFailed", err.Error())
		return err
	}

	executor, err := r.ExecService.GetExecutor(ctx, execInContainer)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to get executor for SMB-W domain join: %v", err)
		r.Recorder.Event(r.cluster, "Warning", "SmbwDomainJoinFailed", errMsg)
		return errors.Wrap(err, "Failed to get executor")
	}

	// Execute: weka smb domain join <username> <password>
	cmd := fmt.Sprintf("weka smb domain join %s %s", username, password)
	_, stderr, err := executor.ExecSensitive(ctx, "JoinSmbwDomain", []string{"bash", "-ce", cmd})
	if err != nil {
		stderrStr := stderr.String()
		// Check if already joined (not an error)
		if containsAlreadyJoined(stderrStr) {
			logger.Info("SMB-W domain already joined")
			return nil
		}

		errMsg := fmt.Sprintf("Failed to join SMB-W domain: %s", stderrStr)
		r.Recorder.Event(r.cluster, "Warning", "SmbwDomainJoinFailed", errMsg)
		return errors.Wrapf(err, "Failed to join SMB-W domain: %s", stderrStr)
	}

	logger.Info("SMB-W domain joined successfully")
	logger.SetStatus(codes.Ok, "SMB-W domain joined")

	return nil
}

// containsAlreadyJoined checks if the error message indicates the domain is already joined
func containsAlreadyJoined(stderr string) bool {
	// Common patterns that indicate domain is already joined
	return contains(stderr, "already joined") ||
		contains(stderr, "already a member") ||
		contains(stderr, "already in domain")
}

// contains is a helper function for case-insensitive string matching
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsIgnoreCase(s, substr))
}

func containsIgnoreCase(s, substr string) bool {
	s, substr = strings.ToLower(s), strings.ToLower(substr)
	return strings.Contains(s, substr)
}

func (r *wekaClusterReconcilerLoop) getSmbwDomainJoinPassword(ctx context.Context, secretName string) (string, error) {
	secret := &corev1.Secret{}
	if err := r.getClient().Get(ctx, client.ObjectKey{
		Name:      secretName,
		Namespace: r.cluster.Namespace,
	}, secret); err != nil {
		return "", errors.Wrapf(err, "failed to get secret %q", secretName)
	}

	// Try common password keys
	passwordKeys := []string{"password", "Password", "PASSWORD"}
	for _, key := range passwordKeys {
		if value, ok := secret.Data[key]; ok {
			return string(value), nil
		}
	}

	return "", errors.Errorf("no password key found in secret %q (tried: %v)", secretName, passwordKeys)
}

// ShouldConfigureSmbwIpRanges returns true if SMB-W IP ranges need to be configured.
// It checks the condition hash against the current spec hash.
func (r *wekaClusterReconcilerLoop) ShouldConfigureSmbwIpRanges() bool {
	// Get target IP ranges from cluster spec
	targetIpRanges := []string{}
	if r.cluster.Spec.SmbwConfig != nil {
		targetIpRanges = r.cluster.Spec.SmbwConfig.IpRanges
	}

	// If no IP ranges configured, skip
	if len(targetIpRanges) == 0 {
		return false
	}

	// Calculate hash of target IP ranges
	currentHash := calculateSmbwIpRangesHash(targetIpRanges)

	// Check if condition exists and hash matches
	cond := meta.FindStatusCondition(r.cluster.Status.Conditions, condition.CondSmbwIpRangesConfigured)
	if cond != nil && cond.Status == metav1.ConditionTrue && cond.Message == currentHash {
		return false // Already configured with current spec
	}

	return true // Needs configuration
}

// EnsureSmbwIpRanges ensures the SMB-W cluster has the correct floating IP ranges.
func (r *wekaClusterReconcilerLoop) EnsureSmbwIpRanges(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureSmbwIpRanges")
	defer end()

	// Get target IP ranges from cluster spec
	targetIpRanges := []string{}
	if r.cluster.Spec.SmbwConfig != nil {
		targetIpRanges = r.cluster.Spec.SmbwConfig.IpRanges
	}

	// Calculate hash of target IP ranges
	currentHash := calculateSmbwIpRangesHash(targetIpRanges)

	// Configure IP ranges
	execInContainer := discovery.SelectActiveContainer(r.containers)
	wekaService := services.NewWekaService(r.ExecService, execInContainer)

	err := wekaService.EnsureSmbwIpRanges(ctx, targetIpRanges)
	if err != nil {
		logger.SetError(err, "Failed to ensure SMB-W IP ranges")
		// Set condition to false on failure
		meta.SetStatusCondition(&r.cluster.Status.Conditions, metav1.Condition{
			Type:    condition.CondSmbwIpRangesConfigured,
			Status:  metav1.ConditionFalse,
			Reason:  "ConfigurationFailed",
			Message: err.Error(),
		})
		if updateErr := r.getClient().Status().Update(ctx, r.cluster); updateErr != nil {
			logger.Error(updateErr, "Failed to update cluster status with error condition")
		}
		return err
	}

	// Update condition with the hash as the message
	meta.SetStatusCondition(&r.cluster.Status.Conditions, metav1.Condition{
		Type:    condition.CondSmbwIpRangesConfigured,
		Status:  metav1.ConditionTrue,
		Reason:  "Configured",
		Message: currentHash,
	})

	// Persist the status update
	if err := r.getClient().Status().Update(ctx, r.cluster); err != nil {
		logger.SetError(err, "Failed to update cluster status with SMB-W IP ranges hash")
		return err
	}

	logger.Info("SMB-W IP ranges configured successfully", "hash", currentHash)
	logger.SetStatus(codes.Ok, "SMB-W IP ranges ensured")

	return nil
}

// calculateSmbwIpRangesHash creates a deterministic hash of IP ranges for change detection
func calculateSmbwIpRangesHash(ipRanges []string) string {
	if len(ipRanges) == 0 {
		return "empty"
	}

	// Sort to ensure consistent hash regardless of order
	sorted := make([]string, len(ipRanges))
	copy(sorted, ipRanges)
	sort.Strings(sorted)

	// Calculate SHA256 hash
	hash := sha256.Sum256([]byte(strings.Join(sorted, ",")))
	return hex.EncodeToString(hash[:])
}
