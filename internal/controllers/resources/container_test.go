package resources

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zerologr"
	"github.com/rs/zerolog"
	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/services/discovery"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func setupLogging(ctx context.Context) (context.Context, func(context.Context) error, error) {
	var logger logr.Logger
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

	shutdown, err := instrumentation.SetupOTelSDK(ctx, "test-weka-operator", "", logger)
	if err != nil {
		return nil, nil, fmt.Errorf("Failed to set up OTel SDK: %v", err)
	}

	ctx, _ = instrumentation.GetLoggerForContext(ctx, &logger, "")
	return ctx, shutdown, nil
}

func TestNewContainerFactory(t *testing.T) {
	nodeInfo := &discovery.DiscoveryNodeInfo{}
	container := &wekav1alpha1.WekaContainer{
		Spec: wekav1alpha1.WekaContainerSpec{
			CpuPolicy: wekav1alpha1.CpuPolicyAuto,
		},
	}
	factory := NewPodFactory(container, nodeInfo)
	if factory == nil {
		t.Errorf("NewPodFactory() returned nil")
	}
}

func TestCreate(t *testing.T) {
	container := testingContainer()
	nodeInfo := &discovery.DiscoveryNodeInfo{}
	factory := NewPodFactory(container, nodeInfo)

	ctx := context.Background()
	ctx, shutdown, err := setupLogging(ctx)
	if err != nil {
		t.Fatalf("Failed to setup logging: %v", err)
	}
	defer shutdown(ctx)

	pod, err := factory.Create(ctx)
	if err != nil {
		t.Errorf("FormCluster() returned error: %v", err)
		return
	}

	if pod == nil {
		t.Errorf("FormCluster() returned nil")
		return
	}

	if pod.Name != "weka-container" {
		t.Errorf("FormCluster() returned pod with name %s", pod.Name)
		return
	}
}

func testingContainer() *wekav1alpha1.WekaContainer {
	return &wekav1alpha1.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "weka-container",
		},
		Spec: wekav1alpha1.WekaContainerSpec{
			CpuPolicy: wekav1alpha1.CpuPolicyManual, // CpuPolicyAuto panics
			CoreIds:   []int{0, 1},
		},
	}
}
