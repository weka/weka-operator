package controllers

import (
	"context"
	"os"
	"testing"

	"github.com/go-logr/zapr"
	prettyconsole "github.com/thessem/zap-prettyconsole"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	uzap "go.uber.org/zap"
)

var pkgCtx context.Context

func TestMain(m *testing.M) {
	os.Setenv("OPERATOR_DEV_MODE", "true")
	ctx := context.Background()

	internalLogger := zapr.NewLogger(prettyconsole.NewLogger(uzap.DebugLevel))
	ctx, _ = instrumentation.GetLoggerForContext(ctx, &internalLogger, "TestReconcile")

	pkgCtx = ctx
	os.Exit(m.Run())
}
