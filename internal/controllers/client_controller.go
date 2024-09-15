/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"

	"go.opentelemetry.io/otel/codes"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type ClientController struct {
	Manager ctrl.Manager
}

func NewClientController(mgr ctrl.Manager) *ClientController {
	return &ClientController{Manager: mgr}
}

// Run is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile

func (c *ClientController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ClientReconcile", "namespace", req.Namespace, "name", req.Name)
	defer end()

	wekaClient, err := c.GetClient(ctx, req)
	if err != nil {
		logger.Error(err, "Failed to get wekaClient")
		return ctrl.Result{}, err
	}
	if wekaClient == nil {
		logger.Info("wekaClient resource not found. Ignoring since object must be deleted")
		return ctrl.Result{}, nil
	}

	logger.SetValues("client_uuid", string(wekaClient.GetUID()))
	steps := ClientReconcileSteps(c.Manager, wekaClient)
	return steps.RunAsReconcilerResponse(ctx)
}

func (c *ClientController) GetClient(ctx context.Context, req ctrl.Request) (*wekav1alpha1.WekaClient, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetClient", "namespace", req.Namespace, "name", req.Name)
	defer end()

	wekaClient := &wekav1alpha1.WekaClient{}
	if err := c.Manager.GetClient().Get(ctx, req.NamespacedName, wekaClient); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("wekaClient resource not found. Ignoring since object must be deleted")
			return nil, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "Failed to get wekaClient")
		return nil, err
	}
	logger.SetStatus(codes.Ok, "Fetched wekaClient")
	return wekaClient, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClientController) SetupWithManager(mgr ctrl.Manager, wrappedReconiler reconcile.Reconciler) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&wekav1alpha1.WekaClient{}).
		Owns(&wekav1alpha1.WekaContainer{}).
		Complete(wrappedReconiler)
}
