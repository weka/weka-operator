package test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	prettyconsole "github.com/thessem/zap-prettyconsole"
	"github.com/weka/go-weka-observability/instrumentation"
	uzap "go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var (
	Verbose         = flag.Bool("verbose", false, "verbose output")
	Debug           = flag.Bool("debug", false, "debug output")
	BlissVersion    = flag.String("bliss-version", "latest", "Bliss version")
	ClusterName     = flag.String("cluster-name", "mbp5", "Cluster name")
	OperatorVersion = flag.String("operator-version", "", "Operator version, leave empty to use local build")
	WekaImage       = flag.String(
		"weka-image",
		"quay.io/weka.io/weka-in-container:4.3.5.105",
		"Weka image",
	)
	QuayUsername = flag.String("quay-username", "", "Quay username")
	QuayPassword = flag.String("quay-password", "", "Quay password")
	Cleanup      = flag.Bool("cleanup", true, "Cleanup cluster")
)

var pkgCtx context.Context

func TestMain(m *testing.M) {
	flag.Parse()

	ctx := context.Background()
	ctx, logger, shutdown := initLogging(ctx)
	defer shutdown(ctx)

	pkgCtx = ctx
	logger.Info("main_test")

	if err := ValidateTestEnvironment(ctx); err != nil {
		logger.Error(err, "Test environment not set up correctly")
		os.Exit(1)
	}

	m.Run()
}

func initLogging(ctx context.Context) (context.Context, logr.Logger, func(context.Context) error) {
	logLevel := uzap.WarnLevel
	if *Verbose {
		logLevel = uzap.InfoLevel
	} else if *Debug {
		logLevel = uzap.DebugLevel
	} else {
		fmt.Println("Verbose output disabled")
	}

	internalLogger := prettyconsole.NewLogger(logLevel)
	logger := zapr.NewLogger(internalLogger)

	shutdown, err := instrumentation.SetupOTelSDK(ctx, "weka-operator", "test", logger)
	if err != nil {
		panic(err)
	}

	// Add logger to context
	ctx, logger = instrumentation.GetLoggerForContext(ctx, &logger, "operator.test")

	log.SetLogger(logger)
	return ctx, logger, shutdown
}
