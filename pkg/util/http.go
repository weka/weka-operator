package util

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type RequestOptions struct {
	AuthHeader string
}

// defaultHTTPClient is a singleton HTTP client with OpenTelemetry instrumentation
// This provides automatic trace propagation (span_id and trace_id injection)
var defaultHTTPClient = &http.Client{
	Transport: otelhttp.NewTransport(http.DefaultTransport),
	Timeout:   10 * time.Second,
}

func SendJsonRequest(ctx context.Context, url string, jsonData []byte, options RequestOptions) (*http.Response, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "SendJsonRequest", "url", url)
	defer end()

	// Create a new HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		logger.SetError(err, "Failed to create request")
		return nil, err
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	if options.AuthHeader != "" {
		req.Header.Set("Authorization", options.AuthHeader)
	}

	// Use otelhttp-instrumented client for automatic trace propagation
	// This will inject trace headers (traceparent, tracestate) automatically
	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		logger.SetError(err, "Failed to send request")
		return resp, err
	}

	// Log response status for observability
	logger.SetValues("status_code", resp.StatusCode)
	return resp, nil
}

func SendGetRequest(ctx context.Context, url string, options RequestOptions) (*http.Response, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "SendGetRequest", "url", url)
	defer end()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		logger.SetError(err, "Failed to create request")
		return nil, err
	}

	if options.AuthHeader != "" {
		req.Header.Set("Authorization", options.AuthHeader)
	}

	// Use otelhttp-instrumented client for automatic trace propagation
	// This will inject trace headers (traceparent, tracestate) automatically
	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		logger.SetError(err, "Failed to send request")
		return resp, err
	}

	// Log response status for observability
	logger.SetValues("status_code", resp.StatusCode)
	return resp, nil
}
