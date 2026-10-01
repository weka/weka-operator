package adhoc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/process"
	"github.com/weka/weka-operator/internal/runtime/results"
)

type kernelizeResult struct {
	Err              *string         `json:"err"`
	Raw              json.RawMessage `json:"raw"`
	Candidates       int             `json:"candidates"`
	FilteredOutInUse int             `json:"filtered_out_in_use"`
	Recovered        int             `json:"recovered"`
	Failed           int             `json:"failed"`
}

type kernelizeFailure struct {
	Err string `json:"err"`
}

type kernelizeOutput struct {
	Summary struct {
		TotalCandidates  int `json:"total_candidates"`
		FilteredOutInUse int `json:"filtered_out_in_use"`
		Recovered        int `json:"recovered"`
		Failed           int `json:"failed"`
	} `json:"summary"`
}

// RunKernelize runs `weka-sign-drive kernelize -J`, which recovers NVMe devices left bound to
// igb_uio by a hard-killed ssdproxy. It needs hostPID to skip drives that are in use.
// A result is always written (unless cancelled): without it the operator cannot tell a failure
// from a slow run.
func RunKernelize(ctx context.Context, runner process.CommandRunner, resultsPath string) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "RunKernelize")
	defer logger.End()

	res, runErr := runner.Run(ctx, process.Command{Path: "/weka-sign-drive", Args: []string{"kernelize", "-J"}})
	var execErr *process.ExecError
	if runErr != nil {
		if !errors.As(runErr, &execErr) || execErr.Kind == process.FailureCancelled {
			return runErr
		}
		logger.Warn("kernelize: weka-sign-drive did not exit cleanly", "err", runErr)
	}

	var out kernelizeOutput
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		detail := strings.TrimSpace(string(res.Stderr))
		if detail == "" {
			detail = fmt.Sprintf("exit code %d", res.ExitCode)
			if execErr != nil && execErr.Kind == process.FailureLaunch {
				detail = runErr.Error()
			}
		}
		return results.Write(ctx, resultsPath, kernelizeFailure{
			Err: fmt.Sprintf("failed to parse kernelize output: %v; %s", err, detail),
		})
	}

	// Non-zero exit with parseable JSON means some devices failed; the counts still apply.
	var errMsg *string
	if res.ExitCode != 0 {
		m := fmt.Sprintf("weka-sign-drive kernelize exited with code %d", res.ExitCode)
		errMsg = &m
	}
	return results.Write(ctx, resultsPath, kernelizeResult{
		Err:              errMsg,
		Raw:              json.RawMessage(res.Stdout),
		Candidates:       out.Summary.TotalCandidates,
		FilteredOutInUse: out.Summary.FilteredOutInUse,
		Recovered:        out.Summary.Recovered,
		Failed:           out.Summary.Failed,
	})
}
