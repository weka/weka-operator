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

// LogSpan is an abstract object that can be used instead of regular loggers and spans
type LogSpan struct {
	Ctx context.Context
	logr.Logger
	trace.Span
	spanName string
	End      func(...trace.SpanEndOption)
}

func NewLogSpanForTest(ctx context.Context, names ...string) LogSpan {
	joinNames := strings.Join(names, ".")
	ctx, span := Tracer.Start(ctx, joinNames)
	zapLog, _ := zap.NewDevelopment()
	return LogSpan{
		Ctx:      ctx,
		Logger:   zapr.NewLogger(zapLog),
		Span:     span,
		End:      span.End,
		spanName: joinNames,
	}
}

func (ls LogSpan) Enabled(level int) bool {
	return ls.Logger.Enabled()
}

func (ls LogSpan) WithName(name string) LogSpan {
	newSpanName := strings.Join([]string{ls.spanName, name}, ".")
	ls.Span.SetName(newSpanName)
	return LogSpan{
		Ctx:      ls.Ctx,
		Logger:   ls.Logger.WithName(name),
		Span:     ls.Span,
		End:      ls.End,
		spanName: newSpanName,
	}
}

func (ls LogSpan) WithValues(keysAndValues ...interface{}) LogSpan {
	if len(keysAndValues)%2 != 0 {
		panic("WithValues must be called with an even number of arguments")
	}

	return LogSpan{
		Logger: ls.Logger.WithValues(keysAndValues...),
		Span:   ls.Span,
		End:    ls.End,
		Ctx:    ls.Ctx,
	}
}

func setAttributesFromKeysAndValues(keysAndValues ...interface{}) []attribute.KeyValue {
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

func (ls LogSpan) Info(msg string, keysAndValues ...interface{}) {
	ls.Logger.Info(msg, keysAndValues...)
	ls.SetAttributes(setAttributesFromKeysAndValues(keysAndValues...)...)
	ls.AddEvent(msg)
}

func (ls LogSpan) Debug(msg string, keysAndValues ...interface{}) {
	if ls.Logger.V(4).Enabled() {
		ls.V(4).Info(msg, keysAndValues...)
	}
	ls.SetAttributes(setAttributesFromKeysAndValues(keysAndValues...)...)
	ls.AddEvent(msg)
}

func (ls LogSpan) InfoWithStatus(code codes.Code, msg string, keysAnValues ...interface{}) {
	ls.Info(msg, keysAnValues...)
	ls.SetAttributes(setAttributesFromKeysAndValues(keysAnValues...)...)
	ls.SetStatus(code, msg)
}

func (ls LogSpan) Error(err error, msg string, keysAndValues ...interface{}) {
	ls.Logger.Error(err, msg, keysAndValues...)
	ls.RecordError(err)
	ls.WithValues("message", msg).SetStatus(codes.Error, msg)
}

func (ls LogSpan) SetAttributes(attrs ...attribute.KeyValue) {
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

func (ls LogSpan) SetPhase(phase string) {
	p := strings.TrimSpace(strings.ToUpper(strings.Replace(phase, " ", "_", -1)))
	ls.Span.SetAttributes(attribute.String("phase", p))
	ls.WithValues("phase", p).Info("Setting phase")
}

func (ls LogSpan) Fatalln(err error, msg string, keysAndValues ...interface{}) {
	ls.Error(err, msg, keysAndValues...)
	ls.End()
	os.Exit(1)
}
