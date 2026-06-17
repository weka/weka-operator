package util

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type RequestOptions struct {
	AuthHeader  string
	GzipBody    bool
	Timeout     time.Duration
	ContentType string
	// Client overrides the shared default client (e.g. to apply custom TLS for
	// a self-signed/on-prem endpoint). When set, Timeout is ignored — the caller
	// is expected to configure the timeout on the client itself.
	Client *http.Client
}

// defaultHTTPClient is a singleton HTTP client with OpenTelemetry instrumentation
// This provides automatic trace propagation (span_id and trace_id injection)
var defaultHTTPClient = &http.Client{
	Transport: otelhttp.NewTransport(http.DefaultTransport),
	Timeout:   10 * time.Second,
}

func SendJsonRequest(ctx context.Context, url string, jsonData []byte, options RequestOptions) (*http.Response, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "SendJsonRequest", "url", url)
	defer logger.End()

	var body []byte
	if options.GzipBody {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		if _, err := gz.Write(jsonData); err != nil {
			logger.SetError(err, "Failed to gzip request body")
			return nil, err
		}
		if err := gz.Close(); err != nil {
			logger.SetError(err, "Failed to close gzip writer")
			return nil, err
		}
		body = buf.Bytes()
	} else {
		body = jsonData
	}

	// Create a new HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
	if err != nil {
		logger.SetError(err, "Failed to create request")
		return nil, err
	}

	// Set headers
	contentType := "application/json"
	if options.ContentType != "" {
		contentType = options.ContentType
	}
	req.Header.Set("Content-Type", contentType)
	if options.GzipBody {
		req.Header.Set("Content-Encoding", "gzip")
	}
	if options.AuthHeader != "" {
		req.Header.Set("Authorization", options.AuthHeader)
	}

	// Prefer a caller-supplied client (e.g. with custom TLS). Otherwise use a
	// per-call client with a custom timeout when specified, so we don't mutate
	// the shared defaultHTTPClient.
	httpClient := defaultHTTPClient
	if options.Client != nil {
		httpClient = options.Client
	} else if options.Timeout > 0 {
		httpClient = &http.Client{
			Transport: defaultHTTPClient.Transport,
			Timeout:   options.Timeout,
		}
	}

	// Use otelhttp-instrumented client for automatic trace propagation
	// This will inject trace headers (traceparent, tracestate) automatically
	resp, err := httpClient.Do(req)
	if err != nil {
		logger.SetError(err, "Failed to send request")
		return resp, err
	}

	// Log response status for observability
	logger.SetValues("status_code", resp.StatusCode)
	return resp, nil
}

func SendGetRequest(ctx context.Context, url string, options RequestOptions) (*http.Response, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "SendGetRequest", "url", url)
	defer logger.End()

	req, err := http.NewRequestWithContext(ctx, "GET", url, http.NoBody)
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
