package wekacluster

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/pkg/util/podexec"
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

// Backend types as they appear in TelemetryExportInfo.Type. The casing the weka CLI emits is
// not pinned down, so compare with strings.EqualFold rather than ==.
const (
	exportTypeSplunk = "splunk"
	exportTypeS3     = "s3"
)

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

// extractNumericID extracts the numeric ID from a TelemetrySinkId<N> string.
// The weka CLI returns IDs like "TelemetrySinkId<0>" but update command expects just "0".
func extractNumericID(fullID string) string {
	// Try to extract number from TelemetrySinkId<N> format
	start := strings.Index(fullID, "<")
	end := strings.Index(fullID, ">")
	if start != -1 && end != -1 && end > start {
		return fullID[start+1 : end]
	}
	// If not in expected format, return as-is
	return fullID
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

func (r *wekaClusterReconcilerLoop) fetchSecret(ctx context.Context, secretName, namespace string) (*v1.Secret, error) {
	secret := &v1.Secret{}
	if err := r.getClient().Get(ctx, client.ObjectKey{
		Name:      secretName,
		Namespace: namespace,
	}, secret); err != nil {
		return nil, errors.Wrapf(err, "failed to get secret %q", secretName)
	}

	return secret, nil
}

// getSecretValue retrieves a value from a secret using the "secretName.keyName" format.
func (r *wekaClusterReconcilerLoop) getSecretValue(ctx context.Context, secretRef, namespace string) (string, error) {
	secretName, keyName, err := parseSecretRef(secretRef)
	if err != nil {
		return "", err
	}

	return r.getSecretKey(ctx, secretName, keyName, namespace)
}

// Keys the s3 export expects inside the secrets it names. Unlike the "secretName.keyName"
// refs, these refs name the whole secret, so the key names are fixed here.
const (
	s3AccessKeyIDKey     = "accessKeyId"
	s3SecretAccessKeyKey = "secretKey"
	s3CACertKey          = "ca.crt"
)

// getSecretKey retrieves one key from a secret named directly, for refs that name the whole
// secret rather than using the "secretName.keyName" form.
func (r *wekaClusterReconcilerLoop) getSecretKey(ctx context.Context, secretName, keyName, namespace string) (string, error) {
	secret, err := r.fetchSecret(ctx, secretName, namespace)
	if err != nil {
		return "", err
	}

	valueBytes, ok := secret.Data[keyName]
	if !ok {
		return "", errors.Errorf("key %q not found in secret %q", keyName, secretName)
	}

	return string(valueBytes), nil
}

// getSecretVersion returns a secret's resourceVersion. It changes on every write, so it marks
// credential rotation for change detection without reading the credential material itself.
func (r *wekaClusterReconcilerLoop) getSecretVersion(ctx context.Context, secretName, namespace string) (string, error) {
	secret, err := r.fetchSecret(ctx, secretName, namespace)
	if err != nil {
		return "", err
	}

	return secret.ResourceVersion, nil
}

// getSecretRefVersion returns the resourceVersion of the secret named by a "secretName.keyName" ref.
func (r *wekaClusterReconcilerLoop) getSecretRefVersion(ctx context.Context, secretRef, namespace string) (string, error) {
	secretName, _, err := parseSecretRef(secretRef)
	if err != nil {
		return "", err
	}

	return r.getSecretVersion(ctx, secretName, namespace)
}

// secretFingerprint renders a secret reference for the telemetry hash as "<ref>@<resourceVersion>".
// An unreadable secret contributes an empty version instead of failing the hash; the reconcile
// that follows reports the error with its own context.
func (r *wekaClusterReconcilerLoop) secretFingerprint(ctx context.Context, ref string, versionOf func(context.Context, string, string) (string, error)) string {
	if ref == "" {
		return "@"
	}
	version, err := versionOf(ctx, ref, r.cluster.Namespace)
	if err != nil {
		return ref + "@"
	}
	return ref + "@" + version
}

// getAuthTokenFromSecret retrieves the auth token from the referenced secret.
func (r *wekaClusterReconcilerLoop) getAuthTokenFromSecret(ctx context.Context, secretRef, namespace string) (string, error) {
	return r.getSecretValue(ctx, secretRef, namespace)
}

// validateExports checks all exports in the spec are valid before any Weka state is modified.
// Each export must have exactly one backend configured, required fields must be present,
// and all referenced secrets must be accessible.
func (r *wekaClusterReconcilerLoop) validateExports(ctx context.Context) error {
	ns := r.cluster.Namespace
	for _, exp := range r.cluster.Spec.Telemetry.Exports {
		if exp.Name == "" {
			return errors.New("export has empty name")
		}
		if len(exp.Sources) == 0 {
			return errors.Errorf("export %q has no sources configured", exp.Name)
		}
		if exp.Splunk == nil && exp.S3 == nil {
			return errors.Errorf("export %q has no backend configured", exp.Name)
		}
		if exp.Splunk != nil && exp.S3 != nil {
			return errors.Errorf("export %q has more than one backend configured, exactly one of splunk or s3 is allowed", exp.Name)
		}
		if exp.Splunk != nil {
			if err := r.validateSplunkExport(ctx, exp.Name, exp.Splunk, ns); err != nil {
				return err
			}
		}
		if exp.S3 != nil {
			if err := r.validateS3Export(ctx, exp.Name, exp.S3, ns); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) validateSplunkExport(ctx context.Context, exportName string, cfg *weka.SplunkExportConfig, ns string) error {
	if cfg.Endpoint == "" {
		return errors.Errorf("export %q: splunk endpoint is required", exportName)
	}
	hasCACert := cfg.CACertSecretRef != nil && *cfg.CACertSecretRef != ""
	if hasCACert && cfg.VerifyWithClusterCACert {
		return errors.Errorf("export %q: caCertSecretRef and verifyWithClusterCACert are mutually exclusive", exportName)
	}
	if cfg.AllowUnverifiedCertificate && (hasCACert || cfg.VerifyWithClusterCACert) {
		return errors.Errorf("export %q: allowUnverifiedCertificate cannot be combined with CA certificate verification", exportName)
	}
	if cfg.AuthTokenSecretRef == "" {
		return errors.Errorf("export %q: splunk authTokenSecretRef is required", exportName)
	}
	if _, err := r.getSecretValue(ctx, cfg.AuthTokenSecretRef, ns); err != nil {
		return errors.Wrapf(err, "failed to get auth token for export %s", exportName)
	}
	if hasCACert {
		if _, err := r.getSecretValue(ctx, *cfg.CACertSecretRef, ns); err != nil {
			return errors.Wrapf(err, "failed to get CA cert for export %s", exportName)
		}
	}
	return nil
}

func (r *wekaClusterReconcilerLoop) validateS3Export(ctx context.Context, exportName string, cfg *weka.S3ExportConfig, ns string) error {
	if cfg.BucketName == "" {
		return errors.Errorf("export %q: s3 bucketName is required", exportName)
	}
	if cfg.Region == "" {
		return errors.Errorf("export %q: s3 region is required", exportName)
	}
	hasCACert := cfg.CACertSecretRef != nil && *cfg.CACertSecretRef != ""
	if hasCACert && cfg.VerifyWithClusterCACert {
		return errors.Errorf("export %q: caCertSecretRef and verifyWithClusterCACert are mutually exclusive", exportName)
	}
	if cfg.AllowUnverifiedCertificate && (hasCACert || cfg.VerifyWithClusterCACert) {
		return errors.Errorf("export %q: allowUnverifiedCertificate cannot be combined with CA certificate verification", exportName)
	}
	if cfg.CredentialsSecretRef == "" {
		return errors.Errorf("export %q: s3 credentialsSecretRef is required", exportName)
	}
	if _, err := r.getSecretKey(ctx, cfg.CredentialsSecretRef, s3AccessKeyIDKey, ns); err != nil {
		return errors.Wrapf(err, "failed to get access key ID for export %s", exportName)
	}
	if _, err := r.getSecretKey(ctx, cfg.CredentialsSecretRef, s3SecretAccessKeyKey, ns); err != nil {
		return errors.Wrapf(err, "failed to get secret access key for export %s", exportName)
	}
	if hasCACert {
		if _, err := r.getSecretKey(ctx, *cfg.CACertSecretRef, s3CACertKey, ns); err != nil {
			return errors.Wrapf(err, "failed to get CA cert for export %s", exportName)
		}
	}
	return nil
}

// writeSecretToTempFile writes a secret value to a temporary file in the pod using ExecSensitive.
// Returns the path to the temporary file.
func (r *wekaClusterReconcilerLoop) writeSecretToTempFile(ctx context.Context, executor podexec.Exec, content, prefix string) (string, error) {
	// Create a temp file path
	tempPath := fmt.Sprintf("/tmp/%s-%d", prefix, time.Now().UnixNano())

	// Use ExecSensitive to write the content to avoid logging sensitive data
	// Base64 encode the content to safely transfer it without shell interpretation issues
	// Use printf '%s' instead of echo to avoid adding a trailing newline
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	cmd := fmt.Sprintf("printf '%%s' '%s' | base64 -d > %s", encoded, tempPath)
	_, stderr, err := executor.ExecSensitive(ctx, fmt.Sprintf("WriteSecret.%s", prefix), []string{"bash", "-c", cmd})
	if err != nil {
		return "", errors.Wrapf(err, "failed to write secret to temp file: %s", stderr.String())
	}

	return tempPath, nil
}

// cleanupTempFile removes a temporary file from the pod.
func (r *wekaClusterReconcilerLoop) cleanupTempFile(ctx context.Context, executor podexec.Exec, path string) {
	if path == "" {
		return
	}
	// Best effort cleanup, don't fail if it doesn't work
	_, _, _ = executor.ExecNamed(ctx, "CleanupTempFile", []string{"bash", "-c", fmt.Sprintf("rm -f %s", path)}) //nolint:errcheck // error is intentionally ignored
}

// calculateTelemetryHash creates a deterministic hash of telemetry config for change detection.
// It detects changes to referenced secrets as well as to the spec itself.
//
// Referenced secrets contribute their ref and their resourceVersion, never their contents:
// this hash is published in the TelemetryConfigured condition, and a rotation bumps the
// version anyway. The ref has to be hashed alongside the version because two keys of one
// secret share a resourceVersion, so repointing a "secretName.keyName" ref at a different
// key would otherwise leave the hash unchanged and the old credential in use.
func (r *wekaClusterReconcilerLoop) calculateTelemetryHash(ctx context.Context, telemetry *weka.TelemetryConfig) string {
	if telemetry == nil || len(telemetry.Exports) == 0 {
		return "empty"
	}

	// Create a sorted representation for consistent hashing
	var parts []string
	for _, exp := range telemetry.Exports {
		part := exp.Name + ":" + strings.Join(exp.Sources, ",")
		if exp.Splunk != nil {
			part += fmt.Sprintf(":splunk:%s:token=%s:cacert=%s:allowUnverified=%t:clusterCACert=%t",
				exp.Splunk.Endpoint,
				r.secretFingerprint(ctx, exp.Splunk.AuthTokenSecretRef, r.getSecretRefVersion),
				r.secretFingerprint(ctx, ptr.Deref(exp.Splunk.CACertSecretRef, ""), r.getSecretRefVersion),
				exp.Splunk.AllowUnverifiedCertificate,
				exp.Splunk.VerifyWithClusterCACert)
		}
		if exp.S3 != nil {
			part += fmt.Sprintf(":s3:%s:%s:%s:creds=%s:cacert=%s:allowUnverified=%t:clusterCACert=%t",
				exp.S3.BucketName,
				exp.S3.Region,
				exp.S3.Endpoint,
				r.secretFingerprint(ctx, exp.S3.CredentialsSecretRef, r.getSecretVersion),
				r.secretFingerprint(ctx, ptr.Deref(exp.S3.CACertSecretRef, ""), r.getSecretVersion),
				exp.S3.AllowUnverifiedCertificate,
				exp.S3.VerifyWithClusterCACert)
		}
		parts = append(parts, part)
	}

	// The flag gates enableAuditDefaultFs, so it must affect the hash - otherwise
	// clearing it leaves audit disabled on the default filesystem forever.
	parts = append(parts, fmt.Sprintf("skipDefaultFs=%t", !r.ShouldCreateDefaultFs()))

	// Sort to ensure consistent hash regardless of order
	sort.Strings(parts)

	// Calculate SHA256 hash
	hash := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(hash[:])
}

// EnsureTelemetry ensures the telemetry exports are configured according to the spec.
// This includes enabling/disabling audit at cluster level and configuring individual exports.
// When exports are removed from spec, they are also removed from Weka.
func (r *wekaClusterReconcilerLoop) EnsureTelemetry(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "ensureTelemetry")
	defer logger.End()

	// Calculate hash of target telemetry config (includes secret content)
	currentHash := r.calculateTelemetryHash(ctx, r.cluster.Spec.Telemetry)

	// Check if already configured with current spec (hash matches)
	condition := meta.FindStatusCondition(r.cluster.Status.Conditions, "TelemetryConfigured")
	if condition != nil && condition.Status == metav1.ConditionTrue && condition.Message == currentHash {
		logger.Info("Telemetry already configured with current spec, skipping")
		return nil
	}

	// Get an active container to execute commands
	execInContainer := discovery.SelectActiveContainer(r.containers)
	if execInContainer == nil {
		return errors.New("no active container found for telemetry configuration")
	}

	executor, err := r.ExecService.GetExecutor(ctx, execInContainer)
	if err != nil {
		return errors.Wrap(err, "failed to get executor for telemetry configuration")
	}

	wekaService := services.NewWekaService(r.ExecService, execInContainer)

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

	if err := r.validateExports(ctx); err != nil {
		logger.SetError(err, "Telemetry exports validation failed")
		r.setTelemetryConditionError(ctx, logger, err)
		return err
	}

	if err := r.disableAutoStartTelemetryContainer(ctx, wekaService); err != nil {
		logger.SetError(err, "Failed to disable auto-start telemetry container")
		r.setTelemetryConditionError(ctx, logger, err)
		return err
	}

	if err := r.enableAuditCluster(ctx, executor); err != nil {
		logger.SetError(err, "Failed to enable audit cluster")
		r.setTelemetryConditionError(ctx, logger, err)
		return err
	}

	if r.ShouldCreateDefaultFs() {
		if err := r.enableAuditDefaultFs(ctx, executor); err != nil {
			logger.SetError(err, "Failed to enable audit on default filesystem")
			r.setTelemetryConditionError(ctx, logger, err)
			return err
		}
	} else {
		logger.Info("Skipping audit on default filesystem, it is not operator-managed")
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

// disableAutoStartTelemetryContainer sets the override to prevent weka from auto-provisioning telemetry container.
// It only adds the override if the most recent entry already has the correct value.
// weka debug override add --force always inserts a new row (not an upsert),
// and Weka applies the last entry, so checking the tail is sufficient.
func (r *wekaClusterReconcilerLoop) disableAutoStartTelemetryContainer(ctx context.Context, svc services.WekaService) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "disableAutoStartTelemetryContainer")
	defer logger.End()

	const key = "auto_start_telemetry_container"
	const val = "false"

	entries, err := svc.ListOverridesByKey(ctx, key)
	if err != nil {
		return errors.Wrap(err, "failed to list auto_start_telemetry_container overrides")
	}

	if len(entries) > 0 && entries[len(entries)-1].Value == val {
		logger.Info("auto_start_telemetry_container override already present, skipping")
		return nil
	}

	if err := svc.AddOverride(ctx, key, val, "weka-operator: disable telemetry container auto-start", true); err != nil {
		return errors.Wrap(err, "failed to disable auto-start telemetry container")
	}
	logger.Info("Auto-start telemetry container disabled successfully")
	return nil
}

// enableAuditCluster runs `weka audit cluster enable`
func (r *wekaClusterReconcilerLoop) enableAuditCluster(ctx context.Context, executor podexec.Exec) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "enableAuditCluster")
	defer logger.End()

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
func (r *wekaClusterReconcilerLoop) enableAuditDefaultFs(ctx context.Context, executor podexec.Exec) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "enableAuditDefaultFs")
	defer logger.End()

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
func (r *wekaClusterReconcilerLoop) listTelemetryExports(ctx context.Context, executor podexec.Exec) ([]TelemetryExportInfo, error) {
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
func (r *wekaClusterReconcilerLoop) reconcileTelemetryExport(ctx context.Context, executor podexec.Exec, desired weka.TelemetryExport, currentByName map[string]TelemetryExportInfo) error {
	// Use prefixed name for the actual export in Weka
	prefixedName := getOperatorExportName(desired.Name)
	_, logger := instrumentation.CreateLogSpan(ctx, "reconcileTelemetryExport", "name", prefixedName)
	defer logger.End()

	if desired.Splunk == nil && desired.S3 == nil {
		return errors.Errorf("export %s has no configuration (splunk or s3 is required)", desired.Name)
	}

	existing, exists := currentByName[prefixedName]

	desiredType := exportTypeSplunk
	if desired.S3 != nil {
		desiredType = exportTypeS3
	}

	// Weka export type is immutable, so keeping a name while switching backends has to go
	// through remove+add - `exports update <type>` against an export of the other type fails
	// on every reconcile and never converges. An empty Type means the CLI reported none, which
	// is not grounds for destroying the export.
	if exists && existing.Type != "" && !strings.EqualFold(existing.Type, desiredType) {
		logger.Info("Export backend type changed, recreating", "name", prefixedName, "from", existing.Type, "to", desiredType)
		if err := r.removeTelemetryExport(ctx, executor, existing.ID, prefixedName); err != nil {
			return errors.Wrapf(err, "failed to remove export %s before switching backend to %s", prefixedName, desiredType)
		}
		exists = false
	}

	if desired.S3 != nil {
		if !exists {
			return r.addS3TelemetryExport(ctx, executor, desired, prefixedName)
		}
		logger.Info("Updating existing telemetry export", "name", prefixedName, "id", existing.ID)
		return r.updateS3TelemetryExport(ctx, executor, desired, existing.ID)
	}

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
func (r *wekaClusterReconcilerLoop) addTelemetryExport(ctx context.Context, executor podexec.Exec, export weka.TelemetryExport, exportName string) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "addTelemetryExport", "name", exportName)
	defer logger.End()

	if export.Splunk == nil {
		return errors.Errorf("cannot add export %s: no splunk configuration provided", exportName)
	}

	// Validate mutual exclusivity: caCertSecretRef (user-provided cert) and verifyWithClusterCACert (weka internal cert) cannot both be set
	hasCACert := export.Splunk.CACertSecretRef != nil && *export.Splunk.CACertSecretRef != ""
	if hasCACert && export.Splunk.VerifyWithClusterCACert {
		return errors.Errorf("export %s: caCertSecretRef and verifyWithClusterCACert are mutually exclusive - use caCertSecretRef for user-provided certificates or verifyWithClusterCACert for weka cluster internal certificate", exportName)
	}

	// Get auth token from secret and write to temp file
	authToken, err := r.getAuthTokenFromSecret(ctx, export.Splunk.AuthTokenSecretRef, r.cluster.Namespace)
	if err != nil {
		return errors.Wrapf(err, "failed to get auth token for export %s", exportName)
	}

	tokenPath, err := r.writeSecretToTempFile(ctx, executor, authToken, "splunk-token")
	if err != nil {
		return errors.Wrapf(err, "failed to write auth token to temp file for export %s", exportName)
	}
	defer r.cleanupTempFile(ctx, executor, tokenPath)

	// Handle CA certificate if configured
	var caCertPath string
	if export.Splunk.CACertSecretRef != nil && *export.Splunk.CACertSecretRef != "" {
		caCert, caErr := r.getSecretValue(ctx, *export.Splunk.CACertSecretRef, r.cluster.Namespace)
		if caErr != nil {
			return errors.Wrapf(caErr, "failed to get CA cert for export %s", exportName)
		}

		caCertPath, caErr = r.writeSecretToTempFile(ctx, executor, caCert, "splunk-cacert")
		if caErr != nil {
			return errors.Wrapf(caErr, "failed to write CA cert to temp file for export %s", exportName)
		}
		defer r.cleanupTempFile(ctx, executor, caCertPath)
	}

	// Build sources string
	sources := strings.Join(export.Sources, ",")

	// Build the command - use double quotes around $(cat) to protect token content from shell interpretation
	// Trim endpoint to handle any trailing newlines from YAML parsing
	endpoint := strings.TrimSpace(export.Splunk.Endpoint)
	cmd := fmt.Sprintf("weka telemetry exports add splunk %s --sources %s --target '%s' --auth-token \"$(cat '%s')\"",
		exportName,
		sources,
		endpoint,
		tokenPath,
	)

	// Add optional certificate flags
	if caCertPath != "" {
		cmd += fmt.Sprintf(" --ca-cert '%s'", caCertPath)
	}
	if export.Splunk.AllowUnverifiedCertificate {
		cmd += " --allow-unverified-certificate yes"
	}
	if export.Splunk.VerifyWithClusterCACert {
		cmd += " --verify-with-cluster-cacert yes"
	}

	_, stderr, err := executor.ExecNamed(ctx, "AddTelemetryExport", []string{"bash", "-c", cmd})
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
func (r *wekaClusterReconcilerLoop) updateTelemetryExport(ctx context.Context, executor podexec.Exec, export weka.TelemetryExport, exportID string) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "updateTelemetryExport", "name", export.Name, "id", exportID)
	defer logger.End()

	if export.Splunk == nil {
		return errors.Errorf("cannot update export %s: no splunk configuration provided", export.Name)
	}

	// Validate mutual exclusivity: caCertSecretRef (user-provided cert) and verifyWithClusterCACert (weka internal cert) cannot both be set
	hasCACert := export.Splunk.CACertSecretRef != nil && *export.Splunk.CACertSecretRef != ""
	if hasCACert && export.Splunk.VerifyWithClusterCACert {
		return errors.Errorf("export %s: caCertSecretRef and verifyWithClusterCACert are mutually exclusive - use caCertSecretRef for user-provided certificates or verifyWithClusterCACert for weka cluster internal certificate", export.Name)
	}

	// Get auth token from secret and write to temp file
	authToken, err := r.getAuthTokenFromSecret(ctx, export.Splunk.AuthTokenSecretRef, r.cluster.Namespace)
	if err != nil {
		return errors.Wrapf(err, "failed to get auth token for export %s", export.Name)
	}

	tokenPath, err := r.writeSecretToTempFile(ctx, executor, authToken, "splunk-token")
	if err != nil {
		return errors.Wrapf(err, "failed to write auth token to temp file for export %s", export.Name)
	}
	defer r.cleanupTempFile(ctx, executor, tokenPath)

	// Handle CA certificate if configured
	var caCertPath string
	if export.Splunk.CACertSecretRef != nil && *export.Splunk.CACertSecretRef != "" {
		caCert, caErr := r.getSecretValue(ctx, *export.Splunk.CACertSecretRef, r.cluster.Namespace)
		if caErr != nil {
			return errors.Wrapf(caErr, "failed to get CA cert for export %s", export.Name)
		}

		caCertPath, caErr = r.writeSecretToTempFile(ctx, executor, caCert, "splunk-cacert")
		if caErr != nil {
			return errors.Wrapf(caErr, "failed to write CA cert to temp file for export %s", export.Name)
		}
		defer r.cleanupTempFile(ctx, executor, caCertPath)
	}

	// Build the command - use double quotes around $(cat) to protect token content from shell interpretation
	// Trim endpoint to handle any trailing newlines from YAML parsing
	// Extract numeric ID from TelemetrySinkId<N> format - weka CLI expects just the number
	endpoint := strings.TrimSpace(export.Splunk.Endpoint)
	numericID := extractNumericID(exportID)
	cmd := fmt.Sprintf("weka telemetry exports update splunk %s --target '%s' --auth-token \"$(cat '%s')\"",
		numericID,
		endpoint,
		tokenPath,
	)

	// Add optional certificate flags
	if caCertPath != "" {
		cmd += fmt.Sprintf(" --ca-cert '%s'", caCertPath)
	}
	if export.Splunk.AllowUnverifiedCertificate {
		cmd += " --allow-unverified-certificate yes"
	}
	if export.Splunk.VerifyWithClusterCACert {
		cmd += " --verify-with-cluster-cacert yes"
	}

	_, stderr, err := executor.ExecNamed(ctx, "UpdateTelemetryExport", []string{"bash", "-c", cmd})
	if err != nil {
		return errors.Wrapf(err, "failed to update telemetry export %s: %s", export.Name, stderr.String())
	}

	logger.Info("Telemetry export updated successfully", "name", export.Name, "id", exportID)
	return nil
}

// s3ExportTarget resolves the --target weka requires for an S3 export. An explicit
// endpoint wins, for S3-compatible object stores. Otherwise it is derived from the
// region: weka rejects an export with no target ("Target is required for Telemetry
// export of type: S3") rather than resolving the AWS endpoint itself, so leaving it
// off is never valid.
func s3ExportTarget(cfg *weka.S3ExportConfig) string {
	if endpoint := strings.TrimSpace(cfg.Endpoint); endpoint != "" {
		return endpoint
	}
	return fmt.Sprintf("https://s3.%s.amazonaws.com", strings.TrimSpace(cfg.Region))
}

// s3ExportFlags writes the s3 credentials and optional CA certificate to temp files in the pod and
// renders the flags shared by `exports add s3` and `exports update s3`. The returned cleanup removes
// those temp files and must be called once the command has run, including on the error paths.
func (r *wekaClusterReconcilerLoop) s3ExportFlags(ctx context.Context, executor podexec.Exec, cfg *weka.S3ExportConfig, exportName string) (flags string, cleanupFn func(), err error) {
	var paths []string
	cleanup := func() {
		for _, path := range paths {
			r.cleanupTempFile(ctx, executor, path)
		}
	}

	writeSecret := func(secretName, keyName, prefix string) (string, error) {
		value, secretErr := r.getSecretKey(ctx, secretName, keyName, r.cluster.Namespace)
		if secretErr != nil {
			return "", errors.Wrapf(secretErr, "failed to get %s for export %s", prefix, exportName)
		}
		path, writeErr := r.writeSecretToTempFile(ctx, executor, value, prefix)
		if writeErr != nil {
			return "", errors.Wrapf(writeErr, "failed to write %s to temp file for export %s", prefix, exportName)
		}
		paths = append(paths, path)
		return path, nil
	}

	accessKeyPath, err := writeSecret(cfg.CredentialsSecretRef, s3AccessKeyIDKey, "s3-access-key-id")
	if err != nil {
		return "", cleanup, err
	}
	secretKeyPath, err := writeSecret(cfg.CredentialsSecretRef, s3SecretAccessKeyKey, "s3-secret-key")
	if err != nil {
		return "", cleanup, err
	}

	// Use double quotes around $(cat) to protect key content from shell interpretation
	// Trim values to handle any trailing newlines from YAML parsing
	flags = fmt.Sprintf(" --bucket-name '%s' --region '%s' --access-key-id \"$(cat '%s')\" --secret-key \"$(cat '%s')\"",
		strings.TrimSpace(cfg.BucketName),
		strings.TrimSpace(cfg.Region),
		accessKeyPath,
		secretKeyPath,
	)

	flags += fmt.Sprintf(" --target '%s'", s3ExportTarget(cfg))

	if cfg.CACertSecretRef != nil && *cfg.CACertSecretRef != "" {
		caCertPath, caErr := writeSecret(*cfg.CACertSecretRef, s3CACertKey, "s3-cacert")
		if caErr != nil {
			return "", cleanup, caErr
		}
		flags += fmt.Sprintf(" --ca-cert '%s'", caCertPath)
	}
	if cfg.AllowUnverifiedCertificate {
		flags += " --allow-unverified-certificate yes"
	}
	if cfg.VerifyWithClusterCACert {
		flags += " --verify-with-cluster-cacert yes"
	}

	return flags, cleanup, nil
}

// addS3TelemetryExport adds a new s3 telemetry export with the given name (should include operator prefix)
func (r *wekaClusterReconcilerLoop) addS3TelemetryExport(ctx context.Context, executor podexec.Exec, export weka.TelemetryExport, exportName string) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "addS3TelemetryExport", "name", exportName)
	defer logger.End()

	flags, cleanup, err := r.s3ExportFlags(ctx, executor, export.S3, exportName)
	defer cleanup()
	if err != nil {
		return err
	}

	cmd := fmt.Sprintf("weka telemetry exports add s3 %s --sources %s%s",
		exportName,
		strings.Join(export.Sources, ","),
		flags,
	)

	_, stderr, err := executor.ExecNamed(ctx, "AddS3TelemetryExport", []string{"bash", "-c", cmd})
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
					return r.updateS3TelemetryExport(ctx, executor, export, exp.ID)
				}
			}
			return errors.Errorf("export %s exists but could not find its ID", exportName)
		}
		return errors.Wrapf(err, "failed to add telemetry export %s: %s", exportName, stderrStr)
	}

	logger.Info("Telemetry export added successfully", "name", exportName)
	return nil
}

// updateS3TelemetryExport updates an existing s3 telemetry export
func (r *wekaClusterReconcilerLoop) updateS3TelemetryExport(ctx context.Context, executor podexec.Exec, export weka.TelemetryExport, exportID string) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "updateS3TelemetryExport", "name", export.Name, "id", exportID)
	defer logger.End()

	flags, cleanup, err := r.s3ExportFlags(ctx, executor, export.S3, export.Name)
	defer cleanup()
	if err != nil {
		return err
	}

	// Extract numeric ID from TelemetrySinkId<N> format - weka CLI expects just the number
	// Sources are omitted: `update --sources` attaches an additional source and fails when the
	// export already has one.
	cmd := fmt.Sprintf("weka telemetry exports update s3 %s%s", extractNumericID(exportID), flags)

	_, stderr, err := executor.ExecNamed(ctx, "UpdateS3TelemetryExport", []string{"bash", "-c", cmd})
	if err != nil {
		return errors.Wrapf(err, "failed to update telemetry export %s: %s", export.Name, stderr.String())
	}

	logger.Info("Telemetry export updated successfully", "name", export.Name, "id", exportID)
	return nil
}

// removeTelemetryExport removes a telemetry export by ID
func (r *wekaClusterReconcilerLoop) removeTelemetryExport(ctx context.Context, executor podexec.Exec, exportID, exportName string) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "removeTelemetryExport", "name", exportName, "id", exportID)
	defer logger.End()

	// Extract numeric ID from TelemetrySinkId<N> format - weka CLI expects just the number
	numericID := extractNumericID(exportID)
	cmd := fmt.Sprintf("weka telemetry exports remove %s --force", numericID)
	_, stderr, err := executor.ExecNamed(ctx, "RemoveTelemetryExport", []string{"bash", "-c", cmd})
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
func (r *wekaClusterReconcilerLoop) disableAuditCluster(ctx context.Context, executor podexec.Exec) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "disableAuditCluster")
	defer logger.End()

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
