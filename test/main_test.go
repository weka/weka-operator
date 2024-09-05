package test

import (
	"context"
	"flag"
	"fmt"
	"testing"

	"github.com/go-logr/zapr"
	prettyconsole "github.com/thessem/zap-prettyconsole"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	uzap "go.uber.org/zap"
)

var (
	Verbose      = flag.Bool("verbose", false, "verbose output")
	Debug        = flag.Bool("debug", false, "debug output")
	BlissVersion = flag.String("bliss-version", "1.11.1", "Bliss version")
)

var pkgCtx context.Context

func TestMain(m *testing.M) {
	flag.Parse()

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

	ctx := context.Background()
	shutdown, err := instrumentation.SetupOTelSDK(ctx)
	if err != nil {
		panic(err)
	}
	defer shutdown(ctx)

	// Add logger to context
	ctx, logger = instrumentation.GetLoggerForContext(ctx, &logger, "operator.test")
	pkgCtx = ctx

	logger.Info("main_test")
	m.Run()
}
