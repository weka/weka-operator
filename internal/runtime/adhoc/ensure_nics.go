package adhoc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/results"
)

type ensureNICsPayload struct {
	Type           string `json:"type"`
	DataNICsNumber int    `json:"dataNICsNumber"`
}

type ensureNICsResult struct {
	Err     *string      `json:"err"`
	NICs    []domain.NIC `json:"nics"`
	Ensured bool         `json:"ensured"`
}

// RunEnsureNICs runs the cloud-helper inside the adhoc container to provision NICs,
// then writes the NIC list to results.json.
// Mirrors Python ensure_nics() at weka_runtime.py:2474.
func RunEnsureNICs(ctx context.Context, cfg *config.Config) error {
	var payload ensureNICsPayload
	if err := json.Unmarshal([]byte(cfg.Instructions.Payload), &payload); err != nil {
		return fmt.Errorf("ensure-nics: parse payload: %w", err)
	}
	if payload.Type != "aws" && payload.Type != "oci" {
		return fmt.Errorf("ensure-nics: payload type %q not supported (must be 'aws' or 'oci')", payload.Type)
	}

	if err := cmdutil.Run(ctx, "mkdir", "-p", "/opt/weka/k8s-scripts"); err != nil {
		return fmt.Errorf("ensure-nics: %w", err)
	}

	helperCmd := fmt.Sprintf("/weka/go-helpers/cloud-helper ensure-nics -n %d", payload.DataNICsNumber)
	out, err := runEnsureNICsHelper(ctx, cfg, helperCmd)
	if err != nil {
		return fmt.Errorf("ensure-nics: cloud-helper: %w", err)
	}

	var parsed struct {
		Metadata struct {
			VNICs []domain.NIC `json:"vnics"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return fmt.Errorf("ensure-nics: parse cloud-helper output: %w", err)
	}

	nics := parsed.Metadata.VNICs
	if len(nics) > 0 {
		nics = nics[1:] // skip first VNIC, matches Python behavior
	}

	return results.Write(ensureNICsResult{Err: nil, Ensured: true, NICs: nics})
}

// runEnsureNICsHelper runs helperCmd inside the adhoc container. When the pod env carries an AWS
// IRSA web-identity (AWS_ROLE_ARN + a readable AWS_WEB_IDENTITY_TOKEN_FILE), the token is streamed
// via stdin into a nested shell that writes it to a tmpfs file and re-exports the identity env vars,
// because `weka local run` does not forward the pod's env into the nested container on its own.
// Falls back to a plain invocation (node instance role via IMDS) otherwise.
// Mirrors Python ensure_nics()'s use_web_identity branch at weka_runtime.py (commit dc0932f3).
func runEnsureNICsHelper(ctx context.Context, cfg *config.Config, helperCmd string) ([]byte, error) {
	logger := instrumentation.CurrentSpanLogger(ctx)

	roleARN := cfg.AWSRoleARN
	tokenFile := cfg.AWSWebIdentityTokenFile
	region := cfg.AWSRegion
	if region == "" {
		region = cfg.AWSDefaultRegion
	}
	tokenPresent := false
	if tokenFile != "" {
		if _, err := os.Stat(tokenFile); err == nil {
			tokenPresent = true
		}
	}

	if roleARN == "" || !tokenPresent {
		logger.Warn("ensure-nics: no IRSA web-identity in pod env, falling back to node instance role via IMDS",
			"role_arn", roleARN, "token_file", tokenFile, "token_present", tokenPresent)
		return cmdutil.Output(ctx, "weka", "local", "run", "--container", "adhoc", "sh", "-c", helperCmd)
	}

	token, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, fmt.Errorf("reading web identity token file %s: %w", tokenFile, err)
	}

	var regionEnv string
	if region != "" {
		regionEnv = fmt.Sprintf("AWS_REGION=%s AWS_DEFAULT_REGION=%s ", region, region)
	}
	nested := fmt.Sprintf(`set -e
umask 177
trap 'rm -f /dev/shm/aws-web-identity-token' EXIT INT TERM
cat > /dev/shm/aws-web-identity-token
if [ ! -s /dev/shm/aws-web-identity-token ]; then
    echo "web identity token not received on stdin - 'weka local run' did not forward stdin" >&2
    exit 1
fi
AWS_ROLE_ARN=%s AWS_WEB_IDENTITY_TOKEN_FILE=/dev/shm/aws-web-identity-token %sAWS_STS_REGIONAL_ENDPOINTS=regional %s`,
		roleARN, regionEnv, helperCmd)

	logger.Info("ensure-nics: streaming IRSA web-identity into nested container via stdin", "role_arn", roleARN)
	// Read the token ourselves and pipe it in directly instead of shelling out to `cat` and an
	// outer `sh -c` (Python's shlex.quote dance): exec.Command's argv form needs no shell quoting.
	return cmdutil.OutputWithStdin(ctx, bytes.NewReader(token), "weka", "local", "run", "--container", "adhoc", "sh", "-c", nested)
}
