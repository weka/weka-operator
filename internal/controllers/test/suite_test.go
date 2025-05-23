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

package test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	//+kubebuilder:scaffold:imports

	"github.com/go-logr/logr"
	"github.com/go-logr/zerologr"
	"github.com/kr/pretty"
	"github.com/rs/zerolog"
	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/wekacluster"
	"github.com/weka/weka-operator/internal/controllers/wekacontainer"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	cfg        *rest.Config
	TestCtx    context.Context
	testCancel context.CancelFunc
	kubectlExe string
)

type TestEnvironment struct {
	Env        *envtest.Environment
	Cancel     context.CancelFunc
	Ctx        context.Context
	Manager    ctrl.Manager
	RestClient rest.Interface
	Client     client.Client
	Logger     logr.Logger
}

func setupLogging(ctx context.Context) (logger logr.Logger, shutdown func(context.Context) error, err error) {
	if os.Getenv("DEBUG") == "true" {
		// Debug logger
		writer := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.TimeOnly}
		zeroLogger := zerolog.New(writer).Level(zerolog.DebugLevel).With().Timestamp().Logger()
		logger = zerologr.New(&zeroLogger)
	} else {
		// Logger that drops/silences messages for unit testing
		zeroLogger := zerolog.Nop()
		logger = zerologr.New(&zeroLogger)
	}

	shutdown, err = instrumentation.SetupOTelSDK(ctx, "test-weka-operator", "", logger)
	if err != nil {
		err = fmt.Errorf("failed to setup OTel SDK: %w", err)
		return
	}

	logf.SetLogger(logger.WithName("test"))
	return
}

func setupTestEnv(ctx context.Context) (testEnv *TestEnvironment, shutdown func(context.Context) error, err error) {
	logger, shutdown, err := setupLogging(ctx)
	if err != nil {
		fmt.Printf("failed to setup logging: %v", err)
		return
	}
	defer shutdown(ctx)

	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		kubebuilderRelease := "1.26.0"
		kubebuilderOs := runtime.GOOS
		kubebuilderArch := runtime.GOARCH
		kubebuilderVersion := fmt.Sprintf("%s-%s-%s", kubebuilderRelease, kubebuilderOs, kubebuilderArch)
		os.Setenv("KUBEBUILDER_ASSETS", filepath.Join("..", "..", "..", "..", "bin", "k8s", kubebuilderVersion))
	}

	os.Setenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc.cluster.local")
	os.Setenv("KUBERNETES_SERVICE_PORT", "443")

	ctx, cancel := context.WithCancel(ctx)
	environment := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "..", "charts", "weka-operator", "crds")},
		ErrorIfCRDPathMissing: true,
		UseExistingCluster:    func(b bool) *bool { return &b }(false),
	}
	testEnv = &TestEnvironment{
		Cancel: cancel,
		Ctx:    ctx,
		Env:    environment,
		Logger: logger,
	}
	cfg, err := testEnv.Env.Start()
	if err != nil {
		fmt.Printf("failed to start test environment: %v", err)
		return
	}
	if err = wekav1alpha1.AddToScheme(scheme.Scheme); err != nil {
		fmt.Printf("failed to add scheme: %v", err)
		return
	}
	testEnv.Client, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		fmt.Printf("failed to create client: %v", err)
		return
	}
	ctrl.SetLogger(logger.WithName("controllers"))

	testEnv.Manager, err = ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	if err != nil {
		fmt.Printf("failed to create manager: %v", err)
		return
	}

	testEnv.RestClient, err = rest.RESTClientFor(cfg)
	if err != nil {
		fmt.Printf("failed to create REST client: %v", err)
		return
	}

	config.Config.DevMode = true
	clusterController := wekacluster.NewWekaClusterController(testEnv.Manager, testEnv.RestClient)
	err = clusterController.SetupWithManager(testEnv.Manager, clusterController)
	if err != nil {
		fmt.Printf("failed to setup WekaCluster controller: %v", err)
		return
	}
	containerController := wekacontainer.NewContainerController(testEnv.Manager, testEnv.RestClient)
	err = containerController.SetupWithManager(testEnv.Manager, containerController)
	if err != nil {
		fmt.Printf("failed to setup Container controller: %v", err)
		return
	}

	go func() {
		testEnv.Manager.Start(testEnv.Ctx)
	}()

	return
}

func teardownTestEnv(testEnv *TestEnvironment) error {
	testEnv.Cancel()
	if err := testEnv.Env.Stop(); err != nil {
		fmt.Printf("failed to stop test environment: %v", err)
		return err
	}

	return nil
}

// waitFor waits for the given condition to be true, or times out.
func waitFor(ctx context.Context, fn func(context.Context) bool) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if fn(ctx) {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return pretty.Errorf("timed out waiting for condition")
	}
	return nil
}
