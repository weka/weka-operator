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
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	uzap "go.uber.org/zap"
	"os"
	"time"
)

var (
	Tracer       = otel.Tracer("weka-operator")
	otlpEndpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	logger       = prettyconsole.NewLogger(uzap.DebugLevel)
)

// SetupOTelSDK bootstraps the OpenTelemetry pipeline.
// If it does not return an error, make sure to call shutdown for proper cleanup.
func SetupOTelSDK(ctx context.Context) (shutdown func(context.Context) error, err error) {
	logger.Info("Setting up OTel SDK")
	shutdownFuncs := make([]func(context.Context) error, 0)

	// shutdown calls cleanup functions registered via shutdownFuncs.
	// The errors from the calls are joined.
	// Each registered cleanup will be invoked once.
	shutdown = func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			if err != nil {
				// Join errors to avoid losing them.
				err = errors.Join(err, fn(ctx))
			}
		}
		shutdownFuncs = []func(context.Context) error{}
		return err
	}

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
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)

	return shutdown, err
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

func newTraceProvider() (*trace.TracerProvider, error) {
	ctx := context.Background()
	logger.Info("Setting up OTel trace provider")
	var traceProvider *trace.TracerProvider

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

		traceProvider = trace.NewTracerProvider(
			trace.WithBatcher(traceExporter,
				// Default is 5s. Set to 1s for demonstrative purposes.
				trace.WithBatchTimeout(time.Second)),
			trace.WithResource(newResource()),
		)
	} else {
		logger.Info("OTLP endpoint not set, using stdout exporter")
		traceExporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, err
		}

		traceProvider = trace.NewTracerProvider(
			trace.WithBatcher(traceExporter, trace.WithBatchTimeout(time.Second/10)),
			trace.WithResource(newResource()),
		)
	}

	return traceProvider, nil
}

func init() {
	ctx := context.Background()
	shutdownFunc, err := SetupOTelSDK(ctx)
	if err != nil {
		logger.Error("failed to set up OTel SDK", uzap.Error(err))
		os.Exit(1)
	} else {
		logger.Info("Successfully set up OTel SDK")
		defer func() {
			err = errors.Join(err, shutdownFunc(ctx))
		}()
	}
	otel.SetTextMapPropagator(propagation.TraceContext{})
}
