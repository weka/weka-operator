package main

import (
	"os"

	"github.com/go-logr/logr"
	zapr "github.com/go-logr/zapr"
	"github.com/pkg/errors"
	prettyconsole "github.com/thessem/zap-prettyconsole"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	nodeLabeller "github.com/weka/weka-operator/internal/app/node-labeller"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
)

type App struct {
	Logger   logr.Logger
	NodeName string
}

func (a *App) Main() error {
	ctrl.SetLogger(a.Logger)

	// Set up Scheme for all resources
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(wekav1alpha1.AddToScheme(scheme))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	if err != nil {
		return errors.Wrap(err, "failed to create manager")
	}

	if err := nodeLabeller.NewController(mgr, a.Logger, a.NodeName).SetupWithManager(mgr); err != nil {
		return errors.Wrap(err, "failed to setup node labeller controller")
	}

	a.Logger.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return errors.Wrap(err, "failed to start manager")
	}

	return nil
}

func main() {
	logger := zapr.NewLogger(prettyconsole.NewLogger(zap.DebugLevel))

	nodeName := os.Getenv("NODE_NAME")
	app := &App{
		Logger:   logger,
		NodeName: nodeName,
	}
	app.Logger.Info("Starting node labeller for node", "name", nodeName)

	// debug env variables
	if err := app.Main(); err != nil {
		app.Logger.Error(err, "Failed to run node labeller")
		os.Exit(1)
	}
}
