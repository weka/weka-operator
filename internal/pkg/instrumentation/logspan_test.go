package instrumentation

import (
	"context"
	"testing"
)

func TestErrorWithSpanName(t *testing.T) {
	ctx, logger := GetLoggerForContext(context.Background(), nil, "test")

	name := GetLogName(ctx)
	if name != "test" {
		t.Errorf("expected logger name to be 'test', got '%s'", name)
	}

	ctx, _ = GetLoggerForContext(ctx, &logger, "test2")
	name = GetLogName(ctx)
	if name != "test.test2" {
		t.Errorf("expected logger name to be 'test.test2', got '%s'", name)
	}
}
