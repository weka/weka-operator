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
	"github.com/weka/weka-operator/internal/app/manager/domain"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"go.uber.org/zap/zapcore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"log"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"time"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/weka/weka-operator/internal/app/manager/controllers"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(wekav1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

type WekaReconciler interface {
	reconcile.Reconciler
	SetupWithManager(mgr ctrl.Manager, reconciler reconcile.Reconciler) error
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var enableClusterApi bool
	tombstoneConfig := controllers.TombstoneConfig{}
	compatibilityConfig := domain.CompatibilityConfig{}

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&enableClusterApi, "enable-cluster-api", false, "Enable Cluster API controllers")
	flag.BoolVar(&tombstoneConfig.EnableTombstoneGc, "enable-tombstone-gc", true, "Enable Tombstone GC")
	flag.DurationVar(&tombstoneConfig.TombstoneGcInterval, "tombstone-gc-interval", 3*time.Second, "GC Interval")
	flag.DurationVar(&tombstoneConfig.TombstoneExpiration, "tombstone-expiration", 10*time.Second, "Tombstone Expiration")
	flag.BoolVar(&compatibilityConfig.CosEnableHugepagesConfig, "enable-cos-hugepages-config", false, "Enable COS Hugepages Config")
	flag.StringVar(&compatibilityConfig.CosHugepageSize, "cos-hugepage-size", "2m", "COS Hugepages size (default 2m)")
	flag.IntVar(&compatibilityConfig.CosHugepagesCount, "cos-hugepage-count", 4000, "COS Hugepages count (default 4000)")
	flag.BoolVar(&compatibilityConfig.CosDisableDriverSigningEnforcement, "disable-cos-driver-signing-enforcement", false, "Disable COS Driver Signing Enforcement")

	ctx := ctrl.SetupSignalHandler()

	opts := zap.Options{
		Development: true,
	}
	zap.Level(zapcore.Level(-2))
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctx = context.WithValue(ctx, "is_root", true)

	ctx, logger := instrumentation.GetLoggerForContext(ctx, nil, "")
	ctrl.SetLogger(logger)

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
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	shutdown, err := instrumentation.SetupOTelSDK(ctx)
	if err != nil {
		logger.Error(err, "Failed to set up OTel SDK")
		os.Exit(1)
	}
	defer func() {
		_ = shutdown(ctx)
	}()

	ctrls := []WekaReconciler{
		controllers.NewClientReconciler(mgr),
		controllers.NewContainerController(mgr, compatibilityConfig),
		controllers.NewWekaClusterController(mgr, compatibilityConfig),
		controllers.NewTombstoneController(mgr, tombstoneConfig),
	}

	setupContextMiddleware := func(next WekaReconciler) reconcile.Reconciler {
		return reconcile.Func(func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
			ctx, _ = instrumentation.GetLoggerForContext(ctx, &logger, "")
			return next.Reconcile(ctx, req)
		})
	}

	for _, c := range ctrls {
		if err = c.SetupWithManager(mgr, setupContextMiddleware(c)); err != nil {
			setupLog.Error(err, "unable to add controller to manager")
			os.Exit(1)
		}

	}

	// Cluster API only enabled explicitly by setting `--enable-cluster-api=true`
	if enableClusterApi {
		if err = (controllers.NewClusterApiController(mgr)).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "ClusterAPI")
			os.Exit(1)
		}
	} else {
		setupLog.Info("Cluster API controllers are disabled by default. Enable them by setting `--enable-cluster-api=true`")
	}

	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	// index additional fields
	// Setup Indexer
	if err := indexOwnerReferenceField(mgr); err != nil {
		log.Fatal("Failed to set up owner reference indexer", err)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func indexOwnerReferenceField(mgr manager.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &wekav1alpha1.WekaContainer{}, "metadata.ownerReferences.uid", func(rawObj client.Object) []string {
		// Grab the job object, extract the owner...
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
