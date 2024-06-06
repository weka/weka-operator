package instrumentation

import (
	"bufio"
	"bytes"
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

func TestGetLogSpan(t *testing.T) {
	buffer := &bytes.Buffer{}
	writer := bufio.NewWriter(buffer)

	ctx := InitTestingLogger(context.Background(), writer)

	_, logger, done := GetLogSpan(ctx, "test-span")
	defer done()

	logger.Info("test message")

	if err := writer.Flush(); err != nil {
		t.Fatalf("failed to flush writer: %v", err)
	}
	logString := buffer.String()
	if !bytes.Contains(buffer.Bytes(), []byte("test message")) {
		t.Errorf("expected log message to contain 'test message', got '%s'", logString)
	}
}
