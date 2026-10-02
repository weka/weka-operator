package results

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/weka/go-weka-observability/instrumentation"
)

// DefaultPath is the results file location used when no config-driven path is available.
const DefaultPath = "/weka-runtime/results.json"

// Write marshals result to JSON and writes it to path, creating parent directories as needed.
func Write(ctx context.Context, path string, result any) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}

	_, logger := instrumentation.CreateLogSpan(ctx, "results.Write", "path", path)
	defer logger.End()
	logger.Info("Writing result", "path", path, "results", string(data))

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
