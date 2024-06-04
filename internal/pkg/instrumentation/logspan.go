package instrumentation

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	prettyconsole "github.com/thessem/zap-prettyconsole"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	uzap "go.uber.org/zap"
)

const (
	ContextLoggerKey = "_spanlogger_logger"
	ContextValuesKey = "_spanlogger_values"
)

// SpanLogger is an abstract object that can be used instead of regular loggers and spans
type SpanLogger struct {
	Ctx context.Context
	logr.Logger
	trace.Span
	spanName string
}

func GetLoggerForContext(ctx context.Context, baseLogger *logr.Logger, name string, keysAndValues ...interface{}) (context.Context, logr.Logger) {
	var logger logr.Logger
	if baseLogger == nil {
		if ctx.Value(ContextLoggerKey) != nil {
			logger = ctx.Value(ContextLoggerKey).(logr.Logger)
		} else {
			initLogger := prettyconsole.NewLogger(uzap.DebugLevel)
			logger = zapr.NewLogger(initLogger)
		}
	} else {
		logger = *baseLogger
	}

	logger = logger.WithValues(keysAndValues...)
	if name != "" {
		logger = logger.WithName(name)
	}
	retCtx := context.WithValue(ctx, ContextLoggerKey, logger)
	return retCtx, logger
}

func GetSpanForContext(ctx context.Context, name string, keysAndValues ...interface{}) (context.Context, trace.Span) {
	ctx, span := Tracer.Start(ctx, name)
	// expand with values saved previously in context
	if ctx.Value(ContextValuesKey) != nil {
		keysAndValues = append(keysAndValues, ctx.Value(ContextValuesKey).([]interface{})...)
	}
	spanAttrs := getAttributesFromKeysAndValues(keysAndValues...)
	span.SetAttributes(spanAttrs...)
	ctx = context.WithValue(ctx, ContextValuesKey, keysAndValues)
	return ctx, span
}

func (ls *SpanLogger) Enabled(level int) bool {
	return ls.Logger.Enabled()
}

//	func (ls *SpanLogger) WithValues(keysAndValues ...interface{}) SpanLogger {
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

func (ls *SpanLogger) Info(msg string, keysAndValues ...interface{}) {
	ls.Logger.Info(msg, keysAndValues...)
	ls.Span.SetAttributes(getAttributesFromKeysAndValues(keysAndValues...)...)
	ls.AddEvent(msg)
}

func (ls *SpanLogger) Debug(msg string, keysAndValues ...interface{}) {
	if ls.Logger.V(4).Enabled() { // TODO: Do we really need this?
		// Why info of Logger does not validate this? Or intention was to hide event as well?
		ls.V(4).Info(msg, keysAndValues...)
	}
	ls.Span.SetAttributes(getAttributesFromKeysAndValues(keysAndValues...)...)
	ls.AddEvent(msg)
}

func (ls *SpanLogger) InfoWithStatus(code codes.Code, msg string, keysAnValues ...interface{}) {
	ls.Info(msg, keysAnValues...)
	ls.SetAttributes(getAttributesFromKeysAndValues(keysAnValues...)...)
	ls.SetStatus(code, msg)
}

func (ls *SpanLogger) Error(err error, msg string, keysAndValues ...interface{}) {
	ls.Logger.Error(err, msg, keysAndValues...)
	ls.RecordError(err)
}

func (ls *SpanLogger) SetError(err error, msg string, keysAndValues ...interface{}) {
	ls.Error(err, msg, keysAndValues...)
	ls.SetStatus(codes.Error, msg)
	// TODO: Validate that error is not set yet
}

func (ls *SpanLogger) SetAttributes(attrs ...attribute.KeyValue) {
	var keyvals []any
	for _, attr := range attrs {
		keyvals = append(keyvals, string(attr.Key))
		keyvals = append(keyvals, attr.Value.Emit())
	}
	if len(keyvals) > 0 {
		ls.Span.SetAttributes(attrs...)
	}
}

func (ls *SpanLogger) SetPhase(phase string) {
	p := strings.TrimSpace(strings.ToUpper(strings.Replace(phase, " ", "_", -1)))
	ls.Span.SetAttributes(attribute.String("phase", p))
}

func (ls *SpanLogger) Fatalln(err error, msg string, keysAndValues ...interface{}) {
	ls.Error(err, msg, keysAndValues...)
	os.Exit(1)
}

func (ls *SpanLogger) SetValues(keysAndValues ...interface{}) {
	ls.Logger = ls.Logger.WithValues(keysAndValues...)
	if ls.Span != nil {
		ls.Span.SetAttributes(getAttributesFromKeysAndValues(keysAndValues...)...)
	}
}

func GetLogSpan(ctx context.Context, name string, keysAndValues ...interface{}) (context.Context, *SpanLogger, func()) {
	if len(keysAndValues)%2 != 0 {
		panic("WithValues must be called with an even number of arguments")
	}

	ctx, logger := GetLoggerForContext(ctx, nil, name, keysAndValues...)
	ctx, span := GetSpanForContext(ctx, name, keysAndValues...)

	if span != nil {
		traceID := span.SpanContext().TraceID().String()
		spanID := span.SpanContext().SpanID().String()
		logger = logger.WithValues("trace_id", traceID, "span_id", spanID)
	}

	ShutdownFunc := func() {
		if span != nil {
			span.End()
		}
		logger.V(4).Info(fmt.Sprintf("%s finished", name))
	}

	ls := SpanLogger{
		Logger:   logger,
		Span:     span,
		spanName: name,
	}

	logger.V(4).Info(fmt.Sprintf("%s called", name))
	return ctx, &ls, ShutdownFunc
}

func GetLogName(ctx context.Context) string {
	_, logger := GetLoggerForContext(ctx, nil, "")

	var name string
	if underlier, ok := logger.GetSink().(zapr.Underlier); ok {
		implLogger := underlier.GetUnderlying()
		name = implLogger.Name()
	}

	return name
}
