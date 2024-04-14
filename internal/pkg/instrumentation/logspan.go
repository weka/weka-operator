package instrumentation

import (
	"context"
	"fmt"
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"os"
	"strings"
)

const ContextLoggerKey = "_spanlogger_logger"

// SpanLogger is an abstract object that can be used instead of regular loggers and spans
type SpanLogger struct {
	Ctx context.Context
	logr.Logger
	trace.Span
	spanName string
	End      func(...trace.SpanEndOption)
}

func GetLoggerForContext(ctx context.Context, baseLogger *logr.Logger) (context.Context, logr.Logger) {
	var logger logr.Logger
	if baseLogger == nil {
		if ctx.Value(ContextLoggerKey) != nil {
			logger = ctx.Value(ContextLoggerKey).(logr.Logger)
		} else {
			zapLog, _ := zap.NewDevelopment()
			logger = zapr.NewLogger(zapLog)
		}
	} else {
		logger = *baseLogger
	}

	retCtx := context.WithValue(ctx, ContextLoggerKey, logger)
	return retCtx, logger
}

func (ls SpanLogger) Enabled(level int) bool {
	return ls.Logger.Enabled()
}

func (ls SpanLogger) WithName(name string) SpanLogger {
	newSpanName := strings.Join([]string{ls.spanName, name}, ".")
	ls.Span.SetName(newSpanName)
	return SpanLogger{
		Ctx:      ls.Ctx,
		Logger:   ls.Logger.WithName(name),
		Span:     ls.Span,
		End:      ls.End,
		spanName: newSpanName,
	}
}

//	func (ls SpanLogger) WithValues(keysAndValues ...interface{}) SpanLogger {
//		if len(keysAndValues)%2 != 0 {
//			panic("WithValues must be called with an even number of arguments")
//		}
//
//		return SpanLogger{
//			Logger: ls.Logger.WithValues(keysAndValues...),
//			Span:   ls.Span,
//			Ctx:    ls.Ctx,
//		}
//	}
func getAttributesFromKeysAndValues(keysAndValues ...interface{}) []attribute.KeyValue {
	if len(keysAndValues)%2 != 0 {
		return []attribute.KeyValue{}
	}
	attrs := make([]attribute.KeyValue, 0, len(keysAndValues)/2)
	for i := 0; i < len(keysAndValues); i += 2 {
		k, ok := keysAndValues[i].(string)
		if !ok {
			continue
		}
		attrs = append(attrs, attribute.String(k, fmt.Sprint(keysAndValues[i+1])))
	}
	return attrs
}

func (ls SpanLogger) Info(msg string, keysAndValues ...interface{}) {
	ls.Logger.Info(msg, keysAndValues...)
	ls.SetAttributes(getAttributesFromKeysAndValues(keysAndValues...)...)
	ls.AddEvent(msg)
}

func (ls SpanLogger) Debug(msg string, keysAndValues ...interface{}) {
	if ls.Logger.V(4).Enabled() {
		ls.V(4).Info(msg, keysAndValues...)
	}
	ls.SetAttributes(getAttributesFromKeysAndValues(keysAndValues...)...)
	ls.AddEvent(msg)
}

func (ls SpanLogger) InfoWithStatus(code codes.Code, msg string, keysAnValues ...interface{}) {
	ls.Info(msg, keysAnValues...)
	ls.SetAttributes(getAttributesFromKeysAndValues(keysAnValues...)...)
	ls.SetStatus(code, msg)
}

func (ls SpanLogger) Error(err error, msg string, keysAndValues ...interface{}) {
	ls.Logger.Error(err, msg, keysAndValues...)
	ls.RecordError(err)
}

func (ls SpanLogger) SetError(err error, msg string, keysAndValues ...interface{}) {
	ls.Error(err, msg, keysAndValues...)
	ls.SetStatus(codes.Error, msg)
	// TODO: Validate that error is not set yet
}

func (ls SpanLogger) SetAttributes(attrs ...attribute.KeyValue) {
	var keyvals []any
	for _, attr := range attrs {
		keyvals = append(keyvals, string(attr.Key))
		keyvals = append(keyvals, attr.Value.Emit())
	}
	if len(keyvals) > 0 {
		ls.V(1).Info("Setting attributes", keyvals...)
		ls.Span.SetAttributes(attrs...)
	}
}

func (ls SpanLogger) SetPhase(phase string) {
	p := strings.TrimSpace(strings.ToUpper(strings.Replace(phase, " ", "_", -1)))
	ls.Span.SetAttributes(attribute.String("phase", p))
	ls.WithValues("phase", p).Info("Setting phase")
}

func (ls SpanLogger) Fatalln(err error, msg string, keysAndValues ...interface{}) {
	ls.Error(err, msg, keysAndValues...)
	ls.End()
	os.Exit(1)
}

func (ls SpanLogger) SetValues(keysAndValues ...interface{}) {
	ls.Logger = ls.Logger.WithValues(keysAndValues...)
	if ls.Span == nil {
		ls.Span.SetAttributes(getAttributesFromKeysAndValues(keysAndValues)...)
	}
}

func GetLogSpan(ctx context.Context, name string, keysAndValues ...interface{}) (context.Context, SpanLogger, func()) {
	if len(keysAndValues)%2 != 0 {
		panic("WithValues must be called with an even number of arguments")
	}

	ctx, logger := GetLoggerForContext(ctx, nil)
	// TODO: Un-global when actually needed
	ctx, span := Tracer.Start(ctx, name)

	logger = logger.WithValues(keysAndValues...)
	spanAttrs := getAttributesFromKeysAndValues(keysAndValues...)

	if span != nil {
		span.SetAttributes(spanAttrs...)
		traceID := span.SpanContext().TraceID().String()
		spanID := span.SpanContext().SpanID().String()
		logger = logger.WithValues("trace_id", traceID, "span_id", spanID)
		logger = logger.WithName(name)
	}

	ShutdownFunc := func() {
		if span != nil {
			span.End()
		}
		logger.V(4).Info(fmt.Sprintf("%s finished", name))
	}

	ls := SpanLogger{
		Logger: logger,
		Span:   span,
	}
	logger.V(4).Info(fmt.Sprintf("%s called", name))
	return ctx, ls, ShutdownFunc
}

func someTest(ctx context.Context) {
	ctx, ls, end := GetLogSpan(ctx, "some-name")
	defer end()

	ls.Info("some message")
}
