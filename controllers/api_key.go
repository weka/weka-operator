package controllers

import (
	"context"

	"github.com/pkg/errors"
	wekav1alpha1 "github.com/weka/weka-operator/api/v1alpha1"
	"github.com/weka/weka-operator/internal/weka_api"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

type ApiKeyReconciler struct {
	*ClientReconciler
}

func NewApiKeyReconciler(c *ClientReconciler) *ApiKeyReconciler {
	return &ApiKeyReconciler{c}
}

func (r *ApiKeyReconciler) Reconcile(ctx context.Context, client *wekav1alpha1.Client) (ctrl.Result, error) {
	r.RecordEvent(v1.EventTypeNormal, "Reconciling", "Reconciling api key")

	usernameRef := client.Spec.WekaUsername
	usernameSecretName := usernameRef.SecretKeyRef.Name
	usernameSecret, err := r.lookupSecret(ctx, namespacedName(client, usernameSecretName))
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "Unable to reconcile API key: username secret")
	}
	usernameByes := usernameSecret.Data[usernameRef.SecretKeyRef.Key]
	username := string(usernameByes)
	r.Logger.Info("Username", "username", username)

	passwordRef := client.Spec.WekaPassword
	passwordSecretName := passwordRef.SecretKeyRef.Name
	passwordSecret, err := r.lookupSecret(ctx, namespacedName(client, passwordSecretName))
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "Unable to reconcile API key: password secret")
	}
	passwordBytes := passwordSecret.Data[passwordRef.SecretKeyRef.Key]
	password := string(passwordBytes)
	r.Logger.Info("Password", "password", password)

	// Using the REST API we can exchange a username and password for an API key
	wekaApi, err := weka_api.NewWekaRestApiClient(client.Spec.BackendIP)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "Unable to reconcile API key")
	}

	if err := wekaApi.Login(ctx, username, password); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "Unable to reconcile API key")
	}

	r.ApiKey = wekaApi.ApiKey

	r.RecordEvent(v1.EventTypeNormal, "Reconciled", "API Key recorded")
	return ctrl.Result{}, nil
}

func namespacedName(client *wekav1alpha1.Client, secretName string) types.NamespacedName {
	return types.NamespacedName{
		Name:      secretName,
		Namespace: client.Namespace,
	}
}

func (r *ApiKeyReconciler) lookupSecret(ctx context.Context, secretName types.NamespacedName) (*v1.Secret, error) {
	secret := &v1.Secret{}
	err := r.Get(ctx, secretName, secret)
	if err != nil {
		return nil, errors.Wrapf(err, "Unable to lookup secret %s", secretName)
	}

	return secret, nil
}
