package instrumentation

import (
	"context"
	"fmt"
	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"os"
	"strings"
)

// LogSpan is an abstract object that can be used instead of regular loggers and spans
type LogSpan struct {
	Ctx context.Context
	logr.Logger
	trace.Span
	End func(...trace.SpanEndOption)
}

func (ls LogSpan) Enabled(level int) bool {
	return ls.Logger.Enabled()
}

func (ls LogSpan) WithName(name string) LogSpan {
	//ctx, span := Tracer.Start(ls.Ctx, name)

	//endFunc := func(opts ...trace.SpanEndOption) {
	//	span.End(opts...)
	//	ls.Span.End(opts...)
	//}
	//return LogSpan{
	//	Ctx:    ctx,
	//	Logger: ls.Logger.WithName(name),
	//	Span:   span,
	//	End:    endFunc,
	//}
	ls.Span.SetName(name)
	return LogSpan{
		Ctx:    ls.Ctx,
		Logger: ls.Logger.WithName(name),
		Span:   ls.Span,
		End:    ls.End,
	}
}

func (ls LogSpan) WithValues(keysAndValues ...interface{}) LogSpan {
	if len(keysAndValues)%2 != 0 {
		panic("WithValues must be called with an even number of arguments")
		return ls
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
	if ls.Logger.V(1).Enabled() {
		ls.V(1).Info(msg, keysAndValues...)
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
