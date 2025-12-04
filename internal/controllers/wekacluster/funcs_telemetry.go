package wekacluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/pkg/util"
)

// TelemetryExportInfo represents an export from weka telemetry exports list -J
type TelemetryExportInfo struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Target  string   `json:"target"`
	Sources []string `json:"sources"`
	Enabled bool     `json:"enabled"`
}

// operatorExportPrefix is the prefix added to all operator-managed telemetry exports.
// This allows distinguishing operator-managed exports from user-created ones.
const operatorExportPrefix = "operator-"

// getOperatorExportName returns the full export name with operator prefix
func getOperatorExportName(name string) string {
	return operatorExportPrefix + name
}

// isOperatorManagedExport returns true if the export name has the operator prefix
func isOperatorManagedExport(name string) bool {
	return strings.HasPrefix(name, operatorExportPrefix)
}

// parseSecretRef parses a secret reference in the format "secretName.keyName"
// and returns the secret name and key name.
func parseSecretRef(ref string) (secretName, keyName string, err error) {
	parts := strings.SplitN(ref, ".", 2)
	if len(parts) != 2 {
		return "", "", errors.Errorf("invalid secret reference format %q, expected 'secretName.keyName'", ref)
	}
	return parts[0], parts[1], nil
}

// getAuthTokenFromSecret retrieves the auth token from the referenced secret.
func (r *wekaClusterReconcilerLoop) getAuthTokenFromSecret(ctx context.Context, secretRef, namespace string) (string, error) {
	secretName, keyName, err := parseSecretRef(secretRef)
	if err != nil {
		return "", err
	}

	secret := &v1.Secret{}
	if err := r.getClient().Get(ctx, client.ObjectKey{
		Name:      secretName,
		Namespace: namespace,
	}, secret); err != nil {
		return "", errors.Wrapf(err, "failed to get secret %q", secretName)
	}

	tokenBytes, ok := secret.Data[keyName]
	if !ok {
		return "", errors.Errorf("key %q not found in secret %q", keyName, secretName)
	}

	return string(tokenBytes), nil
}

// ShouldConfigureTelemetry returns true if telemetry needs to be configured.
// It checks the condition hash against the current spec hash.
func (r *wekaClusterReconcilerLoop) ShouldConfigureTelemetry() bool {
	// Calculate hash of target telemetry config
	currentHash := calculateTelemetryHash(r.cluster.Spec.Telemetry)

	// Check if condition exists and hash matches
	condition := meta.FindStatusCondition(r.cluster.Status.Conditions, "TelemetryConfigured")
	if condition != nil && condition.Status == metav1.ConditionTrue && condition.Message == currentHash {
		return false // Already configured with current spec
	}

	return true // Needs configuration
}

// EnsureTelemetry ensures the telemetry exports are configured according to the spec.
// This includes enabling/disabling audit at cluster level and configuring individual exports.
// When exports are removed from spec, they are also removed from Weka.
func (r *wekaClusterReconcilerLoop) EnsureTelemetry(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureTelemetry")
	defer end()

	// Calculate hash of target telemetry config
	currentHash := calculateTelemetryHash(r.cluster.Spec.Telemetry)

	// Get an active container to execute commands
	execInContainer := discovery.SelectActiveContainer(r.containers)
	if execInContainer == nil {
		return errors.New("no active container found for telemetry configuration")
	}

	executor, err := r.ExecService.GetExecutor(ctx, execInContainer)
	if err != nil {
		return errors.Wrap(err, "failed to get executor for telemetry configuration")
	}

	// Get current exports from weka (needed for both add and remove cases)
	currentExports, err := r.listTelemetryExports(ctx, executor)
	if err != nil {
		logger.SetError(err, "Failed to list telemetry exports")
		r.setTelemetryConditionError(ctx, logger, err)
		return err
	}

	// Build a map of current operator-managed exports by name
	currentExportsByName := make(map[string]TelemetryExportInfo)
	for _, exp := range currentExports {
		if isOperatorManagedExport(exp.Name) {
			currentExportsByName[exp.Name] = exp
		}
	}

	// If telemetry config is nil or empty, remove all operator-managed exports and disable audit
	if r.cluster.Spec.Telemetry == nil || len(r.cluster.Spec.Telemetry.Exports) == 0 {
		// Remove all operator-managed exports (leave user-created ones alone)
		for _, exp := range currentExports {
			if !isOperatorManagedExport(exp.Name) {
				continue
			}
			if err := r.removeTelemetryExport(ctx, executor, exp.ID, exp.Name); err != nil {
				logger.SetError(err, "Failed to remove telemetry export", "name", exp.Name)
				r.setTelemetryConditionError(ctx, logger, err)
				return err
			}
		}

		// Disable audit at cluster level
		if err := r.disableAuditCluster(ctx, executor); err != nil {
			logger.SetError(err, "Failed to disable audit cluster")
			r.setTelemetryConditionError(ctx, logger, err)
			return err
		}

		// Update condition
		meta.SetStatusCondition(&r.cluster.Status.Conditions, metav1.Condition{
			Type:    "TelemetryConfigured",
			Status:  metav1.ConditionTrue,
			Reason:  "NoExportsConfigured",
			Message: currentHash,
		})
		if err := r.getClient().Status().Update(ctx, r.cluster); err != nil {
			logger.SetError(err, "Failed to update cluster status")
			return err
		}
		logger.SetStatus(codes.Ok, "Telemetry exports removed and audit disabled")
		return nil
	}

	if err := r.disableAutoStartTelemetryContainer(ctx, executor); err != nil {
		logger.SetError(err, "Failed to disable auto-start telemetry container")
		r.setTelemetryConditionError(ctx, logger, err)
		return err
	}

	if err := r.enableAuditCluster(ctx, executor); err != nil {
		logger.SetError(err, "Failed to enable audit cluster")
		r.setTelemetryConditionError(ctx, logger, err)
		return err
	}

	if err := r.enableAuditDefaultFs(ctx, executor); err != nil {
		logger.SetError(err, "Failed to enable audit on default filesystem")
		r.setTelemetryConditionError(ctx, logger, err)
		return err
	}

	// Step 3: Build set of desired export names (with operator prefix)
	desiredExportNames := make(map[string]struct{})
	for _, exp := range r.cluster.Spec.Telemetry.Exports {
		desiredExportNames[getOperatorExportName(exp.Name)] = struct{}{}
	}

	// Step 4: Remove operator-managed exports that exist in Weka but not in spec
	for _, exp := range currentExports {
		if !isOperatorManagedExport(exp.Name) {
			continue // Skip user-created exports
		}
		if _, desired := desiredExportNames[exp.Name]; !desired {
			if err := r.removeTelemetryExport(ctx, executor, exp.ID, exp.Name); err != nil {
				logger.SetError(err, "Failed to remove telemetry export", "name", exp.Name)
				r.setTelemetryConditionError(ctx, logger, err)
				return err
			}
		}
	}

	// Step 5: Reconcile desired exports (add or update)
	for _, desiredExport := range r.cluster.Spec.Telemetry.Exports {
		if err := r.reconcileTelemetryExport(ctx, executor, desiredExport, currentExportsByName); err != nil {
			logger.SetError(err, "Failed to reconcile telemetry export", "name", desiredExport.Name)
			r.setTelemetryConditionError(ctx, logger, err)
			return err
		}
	}

	// Update condition with the hash as the message
	meta.SetStatusCondition(&r.cluster.Status.Conditions, metav1.Condition{
		Type:    "TelemetryConfigured",
		Status:  metav1.ConditionTrue,
		Reason:  "Configured",
		Message: currentHash,
	})

	// Persist the status update
	if err := r.getClient().Status().Update(ctx, r.cluster); err != nil {
		logger.SetError(err, "Failed to update cluster status with telemetry hash")
		return err
	}

	logger.Info("Telemetry configured successfully", "hash", currentHash)
	logger.SetStatus(codes.Ok, "Telemetry ensured")

	return nil
}

// disableAutoStartTelemetryContainer sets the override to prevent weka from auto-provisioning telemetry container
func (r *wekaClusterReconcilerLoop) disableAutoStartTelemetryContainer(ctx context.Context, executor util.Exec) error {
	_, logger, end := instrumentation.GetLogSpan(ctx, "disableAutoStartTelemetryContainer")
	defer end()

	cmd := "weka debug override add --key auto_start_telemetry_container --value false --force"
	_, stderr, err := executor.ExecNamed(ctx, "DisableAutoStartTelemetryContainer", []string{"bash", "-ce", cmd})
	if err != nil {
		stderrStr := stderr.String()
		// Check if override already exists (not an error)
		if strings.Contains(stderrStr, "already exists") || strings.Contains(stderrStr, "already set") {
			logger.Info("Auto-start telemetry container override already set")
			return nil
		}
		return errors.Wrapf(err, "failed to disable auto-start telemetry container: %s", stderrStr)
	}

	logger.Info("Auto-start telemetry container disabled successfully")
	return nil
}

// enableTelemetryInfo enables telemetry via config override
func (r *wekaClusterReconcilerLoop) enableTelemetryInfo(ctx context.Context, executor util.Exec) error {
	_, logger, end := instrumentation.GetLogSpan(ctx, "enableTelemetryInfo")
	defer end()

	cmd := "weka debug config override telemetryInfo.enabled true"
	_, stderr, err := executor.ExecNamed(ctx, "EnableTelemetryInfo", []string{"bash", "-ce", cmd})
	if err != nil {
		stderrStr := stderr.String()
		return errors.Wrapf(err, "failed to enable telemetry info: %s", stderrStr)
	}

	logger.Info("Telemetry info enabled successfully")
	return nil
}

// disableTelemetryInfo disables telemetry via config override
func (r *wekaClusterReconcilerLoop) disableTelemetryInfo(ctx context.Context, executor util.Exec) error {
	_, logger, end := instrumentation.GetLogSpan(ctx, "disableTelemetryInfo")
	defer end()

	cmd := "weka debug config override telemetryInfo.enabled false"
	_, stderr, err := executor.ExecNamed(ctx, "DisableTelemetryInfo", []string{"bash", "-ce", cmd})
	if err != nil {
		stderrStr := stderr.String()
		return errors.Wrapf(err, "failed to disable telemetry info: %s", stderrStr)
	}

	logger.Info("Telemetry info disabled successfully")
	return nil
}

// enableAuditCluster runs `weka audit cluster enable`
func (r *wekaClusterReconcilerLoop) enableAuditCluster(ctx context.Context, executor util.Exec) error {
	_, logger, end := instrumentation.GetLogSpan(ctx, "enableAuditCluster")
	defer end()

	cmd := "weka audit cluster enable"
	_, stderr, err := executor.ExecNamed(ctx, "EnableAuditCluster", []string{"bash", "-ce", cmd})
	if err != nil {
		stderrStr := stderr.String()
		// Check if audit is already enabled (not an error)
		if strings.Contains(stderrStr, "already enabled") {
			logger.Info("Audit cluster already enabled")
			return nil
		}
		return errors.Wrapf(err, "failed to enable audit cluster: %s", stderrStr)
	}

	logger.Info("Audit cluster enabled successfully")
	return nil
}

// enableAuditDefaultFs runs `weka audit fs enable default`
func (r *wekaClusterReconcilerLoop) enableAuditDefaultFs(ctx context.Context, executor util.Exec) error {
	_, logger, end := instrumentation.GetLogSpan(ctx, "enableAuditDefaultFs")
	defer end()

	cmd := "weka audit fs enable default"
	_, stderr, err := executor.ExecNamed(ctx, "EnableAuditDefaultFs", []string{"bash", "-ce", cmd})
	if err != nil {
		stderrStr := stderr.String()
		// Check if audit is already enabled on this filesystem (not an error)
		if strings.Contains(stderrStr, "already enabled") {
			logger.Info("Audit already enabled on default filesystem")
			return nil
		}
		return errors.Wrapf(err, "failed to enable audit on default filesystem: %s", stderrStr)
	}

	logger.Info("Audit enabled on default filesystem")
	return nil
}

// listTelemetryExports runs `weka telemetry exports list -J` and parses the result
func (r *wekaClusterReconcilerLoop) listTelemetryExports(ctx context.Context, executor util.Exec) ([]TelemetryExportInfo, error) {
	cmd := "weka telemetry exports list -J"
	stdout, stderr, err := executor.ExecNamed(ctx, "ListTelemetryExports", []string{"bash", "-ce", cmd})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list telemetry exports: %s", stderr.String())
	}

	var exports []TelemetryExportInfo
	if err := json.Unmarshal(stdout.Bytes(), &exports); err != nil {
		return nil, errors.Wrapf(err, "failed to parse telemetry exports: %s", stdout.String())
	}

	return exports, nil
}

// reconcileTelemetryExport reconciles a single telemetry export
func (r *wekaClusterReconcilerLoop) reconcileTelemetryExport(ctx context.Context, executor util.Exec, desired weka.TelemetryExport, currentByName map[string]TelemetryExportInfo) error {
	// Use prefixed name for the actual export in Weka
	prefixedName := getOperatorExportName(desired.Name)
	_, logger, end := instrumentation.GetLogSpan(ctx, "reconcileTelemetryExport", "name", prefixedName)
	defer end()

	// Currently only Splunk is supported
	if desired.Splunk == nil {
		return errors.Errorf("export %s has no configuration (only splunk is currently supported)", desired.Name)
	}

	existing, exists := currentByName[prefixedName]

	if !exists {
		// Need to add the export
		return r.addTelemetryExport(ctx, executor, desired, prefixedName)
	}

	// Export exists - check if we need to update
	// We always update since we can't easily check auth token
	logger.Info("Updating existing telemetry export", "name", prefixedName, "id", existing.ID)
	return r.updateTelemetryExport(ctx, executor, desired, existing.ID)
}

// addTelemetryExport adds a new telemetry export with the given name (should include operator prefix)
func (r *wekaClusterReconcilerLoop) addTelemetryExport(ctx context.Context, executor util.Exec, export weka.TelemetryExport, exportName string) error {
	_, logger, end := instrumentation.GetLogSpan(ctx, "addTelemetryExport", "name", exportName)
	defer end()

	if export.Splunk == nil {
		return errors.Errorf("cannot add export %s: no splunk configuration provided", exportName)
	}

	// Get auth token from secret
	authToken, err := r.getAuthTokenFromSecret(ctx, export.Splunk.AuthTokenSecretRef, r.cluster.Namespace)
	if err != nil {
		return errors.Wrapf(err, "failed to get auth token for export %s", exportName)
	}

	// Build sources string
	sources := strings.Join(export.Sources, ",")

	// weka telemetry exports add splunk <name> --sources <sources> --target <endpoint> --auth-token <token>
	cmd := fmt.Sprintf("weka telemetry exports add splunk %s --sources %s --target %s --auth-token %s",
		exportName,
		sources,
		export.Splunk.Endpoint,
		authToken,
	)

	_, stderr, err := executor.ExecNamed(ctx, "AddTelemetryExport", []string{"bash", "-ce", cmd})
	if err != nil {
		stderrStr := stderr.String()
		// Check if export already exists (race condition handling)
		if strings.Contains(stderrStr, "already used by export") {
			logger.Info("Export already exists, will attempt update", "name", exportName)
			// Get the ID from re-listing
			exports, listErr := r.listTelemetryExports(ctx, executor)
			if listErr != nil {
				return errors.Wrapf(listErr, "failed to list exports after add conflict")
			}
			for _, exp := range exports {
				if exp.Name == exportName {
					return r.updateTelemetryExport(ctx, executor, export, exp.ID)
				}
			}
			return errors.Errorf("export %s exists but could not find its ID", exportName)
		}
		return errors.Wrapf(err, "failed to add telemetry export %s: %s", exportName, stderrStr)
	}

	logger.Info("Telemetry export added successfully", "name", exportName)
	return nil
}

// updateTelemetryExport updates an existing telemetry export
func (r *wekaClusterReconcilerLoop) updateTelemetryExport(ctx context.Context, executor util.Exec, export weka.TelemetryExport, exportID string) error {
	_, logger, end := instrumentation.GetLogSpan(ctx, "updateTelemetryExport", "name", export.Name, "id", exportID)
	defer end()

	if export.Splunk == nil {
		return errors.Errorf("cannot update export %s: no splunk configuration provided", export.Name)
	}

	// Get auth token from secret
	authToken, err := r.getAuthTokenFromSecret(ctx, export.Splunk.AuthTokenSecretRef, r.cluster.Namespace)
	if err != nil {
		return errors.Wrapf(err, "failed to get auth token for export %s", export.Name)
	}

	// weka telemetry exports update splunk <export-id> --target <endpoint> --auth-token <token>
	cmd := fmt.Sprintf("weka telemetry exports update splunk %s --target %s --auth-token %s",
		exportID,
		export.Splunk.Endpoint,
		authToken,
	)

	_, stderr, err := executor.ExecNamed(ctx, "UpdateTelemetryExport", []string{"bash", "-ce", cmd})
	if err != nil {
		return errors.Wrapf(err, "failed to update telemetry export %s: %s", export.Name, stderr.String())
	}

	logger.Info("Telemetry export updated successfully", "name", export.Name, "id", exportID)
	return nil
}

// removeTelemetryExport removes a telemetry export by ID
func (r *wekaClusterReconcilerLoop) removeTelemetryExport(ctx context.Context, executor util.Exec, exportID, exportName string) error {
	_, logger, end := instrumentation.GetLogSpan(ctx, "removeTelemetryExport", "name", exportName, "id", exportID)
	defer end()

	cmd := fmt.Sprintf("weka telemetry exports remove %s --force", exportID)
	_, stderr, err := executor.ExecNamed(ctx, "RemoveTelemetryExport", []string{"bash", "-ce", cmd})
	if err != nil {
		stderrStr := stderr.String()
		// Check if export doesn't exist (already removed)
		if strings.Contains(stderrStr, "not found") || strings.Contains(stderrStr, "does not exist") {
			logger.Info("Telemetry export already removed", "name", exportName)
			return nil
		}
		return errors.Wrapf(err, "failed to remove telemetry export %s: %s", exportName, stderrStr)
	}

	logger.Info("Telemetry export removed successfully", "name", exportName, "id", exportID)
	return nil
}

// disableAuditCluster runs `weka audit cluster disable`
func (r *wekaClusterReconcilerLoop) disableAuditCluster(ctx context.Context, executor util.Exec) error {
	_, logger, end := instrumentation.GetLogSpan(ctx, "disableAuditCluster")
	defer end()

	cmd := "weka audit cluster disable"
	_, stderr, err := executor.ExecNamed(ctx, "DisableAuditCluster", []string{"bash", "-ce", cmd})
	if err != nil {
		stderrStr := stderr.String()
		// Check if audit is already disabled (not an error)
		if strings.Contains(stderrStr, "already disabled") || strings.Contains(stderrStr, "not enabled") {
			logger.Info("Audit cluster already disabled")
			return nil
		}
		return errors.Wrapf(err, "failed to disable audit cluster: %s", stderrStr)
	}

	logger.Info("Audit cluster disabled successfully")
	return nil
}

// calculateTelemetryHash creates a deterministic hash of telemetry config for change detection
func calculateTelemetryHash(telemetry *weka.TelemetryConfig) string {
	if telemetry == nil || len(telemetry.Exports) == 0 {
		return "empty"
	}

	// Create a sorted representation for consistent hashing
	var parts []string
	for _, exp := range telemetry.Exports {
		part := exp.Name + ":" + strings.Join(exp.Sources, ",")
		if exp.Splunk != nil {
			// Note: we don't include auth token in hash for security, but include endpoint
			part += ":splunk:" + exp.Splunk.Endpoint
		}
		parts = append(parts, part)
	}

	// Sort to ensure consistent hash regardless of order
	sort.Strings(parts)

	// Calculate SHA256 hash
	hash := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(hash[:])
}

// setTelemetryConditionError sets the TelemetryConfigured condition to false with error
func (r *wekaClusterReconcilerLoop) setTelemetryConditionError(ctx context.Context, logger *instrumentation.SpanLogger, err error) {
	meta.SetStatusCondition(&r.cluster.Status.Conditions, metav1.Condition{
		Type:    "TelemetryConfigured",
		Status:  metav1.ConditionFalse,
		Reason:  "ConfigurationFailed",
		Message: err.Error(),
	})
	if updateErr := r.getClient().Status().Update(ctx, r.cluster); updateErr != nil {
		logger.Error(updateErr, "Failed to update cluster status with error condition")
	}
}
