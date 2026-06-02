package results

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/weka/go-weka-observability/instrumentation"
)

const defaultResultsPath = "/weka-runtime/results.json"

func resultsPath() string {
	if p := os.Getenv("WEKA_RUNTIME_RESULTS_PATH"); p != "" {
		return p
	}
	return defaultResultsPath
}

func Write(result any) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	path := resultsPath()

	// Mirror Python write_results: logging.info("Writing result into /weka-runtime/results.json, results: \n%s", results)
	_, logger := instrumentation.CreateLogSpan(context.Background(), "results.Write", "path", path)
	defer logger.End()
	logger.Info("Writing result", "path", path, "results", string(data))

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func Read() ([]byte, error) {
	return os.ReadFile(resultsPath())
}
