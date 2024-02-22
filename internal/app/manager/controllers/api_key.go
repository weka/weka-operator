package controllers

import (
	"context"
	"encoding/json"

	"github.com/pkg/errors"
	wekav1alpha1 "github.com/weka/weka-operator/internal/app/manager/api/v1alpha1"
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
	r.RecordEvent(v1.EventTypeNormal, "Reconciling", "Reconciling api key")
	// Client generates a key at startup and puts it in a well known location
	// In order to read this file, we need to use Exec to run cat on the container and then read STDOUT
	stdout, stderr, err := r.Executor.Exec(ctx, []string{"cat", "/root/.weka/auth-token.json"})
	if err != nil {
		r.Logger.Error(err, "Failed to get api key", "stdout", stdout.String(), "stderr", stderr.String())
		return ctrl.Result{}, errors.Wrap(err, "failed to get api key")
	}

	// Parse the JSON
	//   - keys: access_token, refresh_token, token_type
	err = json.Unmarshal(stdout.Bytes(), &r.ApiKey)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to parse api key")
	}

	r.RecordEvent(v1.EventTypeNormal, "Reconciled", "API Key recorded")
	return ctrl.Result{}, nil
}
