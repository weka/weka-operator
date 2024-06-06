package instrumentation

import (
	"context"
	"io"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func InitTestingLogger(ctx context.Context, writer io.Writer) context.Context {
	testingLoggerImpl := newTestingLogger(zapcore.AddSync(writer))
	baseLogger := zapr.NewLogger(testingLoggerImpl)
	ctx, _ = GetLoggerForContext(ctx, &baseLogger, "test")
	return ctx
}

func newTestingLogger(writer zapcore.WriteSyncer) *zap.Logger {
	encoder := zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.AddSync(writer), zapcore.DebugLevel)
	return zap.New(core, zap.WithCaller(true))
}
