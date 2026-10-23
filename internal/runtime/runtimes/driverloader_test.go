package runtimes

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteLoaderResultReturnsWriteError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeLoaderResult(context.Background(), filepath.Join(blocker, "results.json"), loaderResult{DriversLoaded: true}); err == nil {
		t.Fatal("expected an error when the results path cannot be written")
	}
	if err := writeLoaderResult(context.Background(), filepath.Join(dir, "results.json"), loaderResult{DriversLoaded: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
