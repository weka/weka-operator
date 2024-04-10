package instrumentation

import (
	"context"
	"errors"
	prettyconsole "github.com/thessem/zap-prettyconsole"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	trace "go.opentelemetry.io/otel/trace"
	uzap "go.uber.org/zap"
	"os"
	"time"
)

var (
	Tracer       = otel.Tracer("weka-operator")
	otlpEndpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	//serviceName  = os.Getenv("OTEL_SERVICE_NAME")
	//insecure     = os.Getenv("OTEL_INSECURE_MODE")
	logger = prettyconsole.NewLogger(uzap.DebugLevel)
)

// SetupOTelSDK bootstraps the OpenTelemetry pipeline.
// If it does not return an error, make sure to call shutdown for proper cleanup.
func SetupOTelSDK(ctx context.Context) (shutdown func(context.Context) error, err error) {
	logger.Info("Setting up OTel SDK")

	// handleErr calls shutdown for cleanup and makes sure that all errors are returned.
	handleErr := func(inErr error) {
		err = errors.Join(inErr, shutdown(ctx))
	}

	// Set up propagator.
	prop := newPropagator()
	otel.SetTextMapPropagator(prop)

	// Set up trace provider.
	logger.Info("Setting up OTel trace provider")
	tracerProvider, err := newTraceProvider()
	if err != nil {
		handleErr(err)
		return shutdown, err
	}
	otel.SetTracerProvider(tracerProvider)

	return tracerProvider.Shutdown, err
}

func newResource() *resource.Resource {
	r, _ := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String("weka-operator"),
			semconv.ServiceVersionKey.String("v1.0.0"),
			attribute.String("environment", "demo"),
		),
	)
	return r
}

func newPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

func newTraceProvider() (*tracesdk.TracerProvider, error) {
	ctx := context.Background()
	logger.Info("Setting up OTel trace provider")
	var traceProvider *tracesdk.TracerProvider

	if otlpEndpoint != "" {
		logger.Info("OTLP endpoint set to " + otlpEndpoint)
		traceExporter, err := otlptracegrpc.New(ctx,
			otlptracegrpc.WithInsecure(),
			otlptracegrpc.WithTimeout(5*time.Second),
			otlptracegrpc.WithEndpointURL(otlpEndpoint),
		)
		if err != nil {
			logger.Error("failed to create OTLP trace exporter", uzap.String("error", err.Error()))
			return nil, err
		}

		traceProvider = tracesdk.NewTracerProvider(
			tracesdk.WithBatcher(traceExporter,
				// Default is 5s. Set to 1s for demonstrative purposes.
				tracesdk.WithBatchTimeout(time.Second)),
			tracesdk.WithResource(newResource()),
		)
	} else {
		logger.Info("OTLP endpoint not set, using stdout exporter")
		traceExporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, err
		}

		traceProvider = tracesdk.NewTracerProvider(
			tracesdk.WithBatcher(traceExporter, tracesdk.WithBatchTimeout(time.Second/10)),
			tracesdk.WithResource(newResource()),
		)
	}

	return traceProvider, nil
}

func NewContextWithTraceID(ctx context.Context, tracer trace.Tracer, spanName, traceIDStr string) context.Context {
	traceID, _ := trace.TraceIDFromHex(traceIDStr)
	spanID, _ := trace.SpanIDFromHex("0000000000000001") // Example span ID; typically this would also come from external data
	if tracer == nil {
		tracer = Tracer
	}

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled, // Be sure to set the flags to reflect the sampling status
	})

	ctx = trace.ContextWithRemoteSpanContext(ctx, sc)
	_, span := tracer.Start(ctx, spanName)
	return trace.ContextWithSpan(ctx, span)
}
