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

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"k8s.io/apimachinery/pkg/util/uuid"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/controllers"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	//+kubebuilder:scaffold:imports
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(wekav1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

type WekaReconciler interface {
	reconcile.Reconciler
	SetupWithManager(mgr ctrl.Manager, reconciler reconcile.Reconciler) error
	RunGC(ctx context.Context)
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var enableClusterApi bool
	tombstoneConfig := controllers.TombstoneConfig{}

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&enableClusterApi, "enable-cluster-api", false, "Enable Cluster API controllers")
	flag.BoolVar(&tombstoneConfig.EnableTombstoneGc, "enable-tombstone-gc", true, "Enable Tombstone GC")
	flag.DurationVar(&tombstoneConfig.TombstoneGcInterval, "tombstone-gc-interval", 3*time.Second, "GC Interval")
	flag.DurationVar(&tombstoneConfig.TombstoneExpiration, "tombstone-expiration", 10*time.Second, "Tombstone Expiration")
	flag.BoolVar(&tombstoneConfig.DeleteOnNodeMissing, "allow-tombstone-delete-on-node-missing", false, "Allow deletion of tombstones when node is not anymore a part of the cluster")

	flag.Parse()

	ctx := ctrl.SetupSignalHandler()
	ctx = context.WithValue(ctx, "is_root", true)

	// initialize root logger and put it into context
	logr := instrumentation.NewZerologrWithLoggerNameInsteadCaller()

	deploymentIdentifier := ""
	deploymentIdentifier = os.Getenv("OTEL_DEPLOYMENT_IDENTIFIER")
	if deploymentIdentifier == "" {
		deploymentIdentifier = os.Getenv("POD_UID")
	}
	if deploymentIdentifier == "" {
		// local mode? Generating new one with dev- prefix
		deploymentIdentifier = "dev-" + string(uuid.NewUUID())
		fmt.Println("OTEL_DEPLOYMENT_IDENTIFIER or POD_UID are not set, using generated one:", deploymentIdentifier)
	}
	fmt.Println("Using " + deploymentIdentifier + " as deployment identifier")

	ctx, logger := instrumentation.GetLoggerForContext(ctx, &logr, "", "deployment_identifier", deploymentIdentifier)
	// HACK: Need to expand go observability lib to support keyvaluelist  on SetupOTEL level
	ctx = context.WithValue(ctx, instrumentation.ContextValuesKey{}, []any{"deployment_identifier", deploymentIdentifier})
	ctrl.SetLogger(logger)
	klog.SetLogger(logger)

	logger.Info("flags", "metricsAddr", metricsAddr, "probeAddr", probeAddr, "enableLeaderElection", enableLeaderElection, "enableClusterApi", enableClusterApi, "tombstoneConfig", tombstoneConfig)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:    metricsAddr,
			ExtraHandlers:  nil,
			FilterProvider: nil,
			CertDir:        "",
			CertName:       "",
			KeyName:        "",
			TLSOpts:        nil,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "ad0b5146.weka.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		logger.Error(err, "unable to start manager")
		os.Exit(1)
	}

	version := os.Getenv("VERSION")
	shutdown, err := instrumentation.SetupOTelSDK(ctx, "weka-operator", version, logger)
	if err != nil {
		logger.Error(err, "Failed to set up OTel SDK")
		os.Exit(1)
	}
	defer func() {
		_ = shutdown(ctx)
	}()

	ctrls := []WekaReconciler{
		controllers.NewClientController(mgr),
		controllers.NewContainerController(mgr),
		controllers.NewWekaClusterController(mgr),
		controllers.NewTombstoneController(mgr, tombstoneConfig),
		controllers.NewWekaPolicyController(mgr),
		controllers.NewWekaManualOperationController(mgr),
	}

	setupContextMiddleware := func(next WekaReconciler) reconcile.Reconciler {
		return reconcile.Func(func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
			localCtx, _ := instrumentation.GetLoggerForContext(ctx, &logger, "")
			// HACK: Need to expand go observability lib to support keyvaluelist  on SetupOTEL level
			localCtx = context.WithValue(localCtx, instrumentation.ContextValuesKey{}, []any{"deployment_identifier", deploymentIdentifier})
			return next.Reconcile(localCtx, req)
		})
	}

	for _, c := range ctrls {
		if err = c.SetupWithManager(mgr, setupContextMiddleware(c)); err != nil {
			logger.Error(err, "unable to add controller to manager")
			os.Exit(1)
		}
		// Run GC for each controller (if implemented)
		go c.RunGC(ctx)
	}

	// Cluster API only enabled explicitly by setting `--enable-cluster-api=true`
	if enableClusterApi {
		if err = (controllers.NewClusterApiController(mgr)).SetupWithManager(mgr); err != nil {
			logger.Error(err, "unable to create controller", "controller", "ClusterAPI")
			os.Exit(1)
		}
	} else {
		logger.Info("Cluster API controllers are disabled by default. Enable them by setting `--enable-cluster-api=true`")
	}

	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	// index additional fields
	// Setup Indexer
	if err := setupContainerIndexes(ctx, mgr); err != nil {
		logger.Error(err, "Failed to set up owner reference indexer")
		os.Exit(1)
	}

	logger.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		logger.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func setupContainerIndexes(ctx context.Context, mgr manager.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(ctx, &corev1.Pod{}, "spec.nodeName", func(rawObj client.Object) []string {
		pod := rawObj.(*corev1.Pod)
		return []string{pod.Spec.NodeName}
	}); err != nil {
		return err
	}

	if err := mgr.GetFieldIndexer().IndexField(ctx, &wekav1alpha1.WekaContainer{}, "metadata.uid", func(rawObj client.Object) []string {
		wekaContainer := rawObj.(*wekav1alpha1.WekaContainer)
		return []string{string(wekaContainer.UID)}
	}); err != nil {
		return err
	}

	if err := mgr.GetFieldIndexer().IndexField(ctx, &wekav1alpha1.WekaContainer{}, "metadata.ownerReferences.uid", func(rawObj client.Object) []string {
		wekaContainer := rawObj.(*wekav1alpha1.WekaContainer)
		owner := metav1.GetControllerOf(wekaContainer)
		if owner == nil {
			return nil
		}
		return []string{string(owner.UID)}
	}); err != nil {
		return err
	}

	return nil
}

type SpecificLevelWriter struct {
	io.Writer
	Levels []zerolog.Level
}

func (w SpecificLevelWriter) WriteLevel(level zerolog.Level, p []byte) (int, error) {
	for _, l := range w.Levels {
		if l == level {
			return w.Write(p)
		}
	}
	return len(p), nil
}
