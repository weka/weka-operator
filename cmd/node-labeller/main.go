package main

import (
	"os"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	nodeLabeller "github.com/weka/weka-operator/internal/app/node-labeller"
	"go.uber.org/zap/zapcore"
	ctrl "sigs.k8s.io/controller-runtime"
	zapr "sigs.k8s.io/controller-runtime/pkg/log/zap"
)

type App struct {
	Logger   logr.Logger
	NodeName string
}

func (a *App) Main() error {
	ctrl.SetLogger(a.Logger)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{})
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
	logger := zapr.New(zapr.UseDevMode(true), zapr.Level(zapcore.DebugLevel))

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
