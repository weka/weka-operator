package controllers

import (
	"context"
	"encoding/json"
	"github.com/weka/weka-operator/controllers/resources"
	"go.opentelemetry.io/otel/codes"

	"github.com/pkg/errors"
	wekav1alpha1 "github.com/weka/weka-operator/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

type ApiKeyReconciler struct {
	*ClientReconciler
	Executor Executor
	ApiKey   string
}

func NewApiKeyReconciler(c *ClientReconciler, executor Executor) *ApiKeyReconciler {
	return &ApiKeyReconciler{c, executor, ""}
}

func (r *ApiKeyReconciler) Reconcile(ctx context.Context, client *wekav1alpha1.Client) (ctrl.Result, error) {
	ctx, span := resources.Tracer.Start(ctx, "reconcile_api_key")
	defer span.End()
	span.AddEvent("Reconciling api key")
	r.RecordEvent(v1.EventTypeNormal, "Reconciling", "Reconciling api key")
	// Client generates a key at startup and puts it in a well known location
	// In order to read this file, we need to use Exec to run cat on the container and then read STDOUT
	stdout, stderr, err := r.Executor.Exec(ctx, []string{"cat", "/root/.weka/auth-token.json"})
	if err != nil {
		r.Logger.Error(err, "Failed to get api key", "stdout", stdout.String(), "stderr", stderr.String())
		span.SetStatus(codes.Error, "Failed to get api key")
		span.RecordError(err)
		return ctrl.Result{}, errors.Wrap(err, "failed to get api key")
	}

	// Parse the JSON
	//   - keys: access_token, refresh_token, token_type
	err = json.Unmarshal(stdout.Bytes(), &r.ApiKey)
	if err != nil {
		span.SetStatus(codes.Error, "Failed to parse api key")
		span.RecordError(err)
		return ctrl.Result{}, errors.Wrap(err, "failed to parse api key")
	}

	r.RecordEvent(v1.EventTypeNormal, "Reconciled", "API Key recorded")
	span.AddEvent("API Key recorded")
	span.SetStatus(codes.Ok, "API Key recorded")
	return ctrl.Result{}, nil
}
