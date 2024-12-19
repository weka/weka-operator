package util

import (
	"bytes"
	"context"
	"net/http"

	"github.com/weka/go-weka-observability/instrumentation"
)

type RequestOptions struct {
	AuthHeader string
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

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		logger.SetError(err, "Failed to send request")
		return resp, err
	}
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

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		logger.SetError(err, "Failed to send request")
		return resp, err
	}
	return resp, nil
}
