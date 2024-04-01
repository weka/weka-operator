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
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/go-logr/zapr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	prettyconsole "github.com/thessem/zap-prettyconsole"
	uzap "go.uber.org/zap"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	cfg        *rest.Config
	k8sClient  client.Client
	k8sManager ctrl.Manager
	testEnv    *envtest.Environment
	TestCtx    context.Context
	testCancel context.CancelFunc
	kubectlExe string
)

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	// logger := zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true))
	logger := zapr.NewLogger(prettyconsole.NewLogger(uzap.DebugLevel))
	logf.SetLogger(logger.WithName("test"))
	TestCtx, testCancel = context.WithCancel(context.TODO())

	var err error
	kubectlExe, err = exec.LookPath("kubectl")
	Expect(err).NotTo(HaveOccurred())

	By("bootstrapping test environment")
	Expect(os.Setenv("OPERATOR_DEV_MODE", "true")).To(Succeed())
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "..", "charts", "weka-operator", "crds")},
		ErrorIfCRDPathMissing: true,
		UseExistingCluster:    func(b bool) *bool { return &b }(false),
	}

	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = wekav1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	Expect(err).NotTo(HaveOccurred())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	ctrl.SetLogger(logger.WithName("controllers"))
	k8sManager, err = ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).NotTo(HaveOccurred())

	err = (NewWekaClusterController(k8sManager).SetupWithManager(k8sManager))
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme
	go func() {
		defer GinkgoRecover()
		err = k8sManager.Start(TestCtx)
		Expect(err).ToNot(HaveOccurred(), "failed to start manager")
	}()
})

var _ = AfterSuite(func() {
	testCancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
