package resources

import (
	"context"
	"errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"os"
	ctrl "sigs.k8s.io/controller-runtime"
	"time"
)

var (
	Tracer   = otel.Tracer("weka-operator")
	Meter    = otel.Meter("weka-operator")
	setupLog = ctrl.Log.WithName("setup")

	tracingEndpoint = "https://listener-eu.logz.io:8053"
	tracingToken    = "mLuXNMTyFCYiagGxNApjaTMbcHjpPBQq"

	bsp sdktrace.SpanProcessor
)

// SetupOTelSDK bootstraps the OpenTelemetry pipeline.
// If it does not return an error, make sure to call shutdown for proper cleanup.
func SetupOTelSDK(ctx context.Context) (shutdown func(context.Context) error, err error) {
	setupLog.Info("Setting up OTel SDK")
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
	tracerProvider, err := newTraceProvider()
	if err != nil {
		handleErr(err)
		return shutdown, err
	}
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)

	// Set up meter provider.
	meterProvider, err := newMeterProvider()
	if err != nil {
		handleErr(err)
		return shutdown, err
	}
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)

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
	traceExporter, err := stdouttrace.New(
		stdouttrace.WithPrettyPrint())
	if err != nil {
		return nil, err
	}

	traceProvider := trace.NewTracerProvider(
		trace.WithBatcher(traceExporter,
			// Default is 5s. Set to 1s for demonstrative purposes.
			trace.WithBatchTimeout(time.Second)),
		trace.WithSpanProcessor(bsp),
		trace.WithResource(newResource()),
	)
	return traceProvider, nil
}

func newMeterProvider() (*metric.MeterProvider, error) {
	metricExporter, err := stdoutmetric.New()
	if err != nil {
		return nil, err
	}

	meterProvider := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExporter,
			// Default is 1m. Set to 3s for demonstrative purposes.
			metric.WithInterval(3*time.Second))),
	)
	return meterProvider, nil
}

func init() {
	ctx := context.Background()
	shutdownFunc, err := SetupOTelSDK(ctx)
	if err != nil {
		setupLog.Error(err, "failed to set up OTel SDK")
		os.Exit(1)
	} else {
		defer func() {
			err = errors.Join(err, shutdownFunc(ctx))
		}()
	}
	exporter, err := newHTTPExporter(ctx)
	if err != nil {
		panic(err)
	}
	bsp = sdktrace.NewBatchSpanProcessor(exporter)
	otel.SetTextMapPropagator(propagation.TraceContext{})
}

func newHTTPExporter(ctx context.Context) (*otlptrace.Exporter, error) {
	headers := make(map[string]string)
	headers["X-Api-Key"] = tracingToken
	headers["api-key"] = tracingToken
	return otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(tracingEndpoint),
		otlptracehttp.WithHeaders(headers),
	)
}
