package results

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func Read() ([]byte, error) {
	return os.ReadFile(resultsPath())
}
