package discovery

import (
	"context"
	"os"
	"testing"

	"github.com/go-logr/zapr"
	prettyconsole "github.com/thessem/zap-prettyconsole"
	"github.com/weka/go-weka-observability/instrumentation"
	uzap "go.uber.org/zap"
)

var pkgCtx context.Context

func TestMain(m *testing.M) {
	os.Setenv("OPERATOR_DEV_MODE", "true")
	ctx := context.Background()

	internalLogger := zapr.NewLogger(prettyconsole.NewLogger(uzap.DebugLevel))
	ctx, _ = instrumentation.GetLoggerForContext(ctx, &internalLogger, "TestReconcile")

	shutdown, err := instrumentation.SetupOTelSDK(ctx, "internal/services/discovery", "0.0.1", internalLogger)
	if err != nil {
		panic(err)
	}
	defer shutdown(ctx)

	pkgCtx = ctx
	os.Exit(m.Run())
}
