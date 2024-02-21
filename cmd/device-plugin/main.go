package main

import (
	"fmt"
	"os"

	"github.com/kubevirt/device-plugin-manager/pkg/dpm"
	device_plugin "github.com/weka/weka-operator/internal/app/device-plugin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type App struct {
	Logger *zap.SugaredLogger
}

func (app *App) Main() error {
	lister := device_plugin.NewDriveLister(app.Logger)
	manager := dpm.NewManager(lister)
	manager.Run()
	return nil
}

func main() {
	stdout := zapcore.Lock(os.Stdout)
	core := zapcore.NewTee(
		zapcore.NewCore(zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig()), stdout, zap.DebugLevel),
	)
	logger := zap.New(core)
	defer logger.Sync()

	app := &App{
		Logger: logger.Sugar(),
	}
	app.Logger.Info("Starting device plugin")

	if err := app.Main(); err != nil {
		fmt.Fprintf(os.Stderr, "Device plugin failure: %v\n", err)
		os.Exit(1)
	}
}
