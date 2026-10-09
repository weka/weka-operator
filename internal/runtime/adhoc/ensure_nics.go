package adhoc

import (
	"context"
	"encoding/json"
	"fmt"

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

	script := fmt.Sprintf(`set -e
mkdir -p /opt/weka/k8s-scripts
weka local run --container adhoc /weka/go-helpers/cloud-helper ensure-nics -n %d`, payload.DataNICsNumber)

	out, err := cmdutil.Output(ctx, "sh", "-c", script)
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
