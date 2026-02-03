package drivers

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-logr/zerologr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rs/zerolog"
	"github.com/weka/go-weka-observability/instrumentation"
	corev1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
)

var otelShutdown func(context.Context) error

func TestDrivers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Drivers Suite")
}

var _ = BeforeSuite(func() {
	ctx := context.Background()
	writer := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.TimeOnly}
	zeroLogger := zerolog.New(writer).Level(zerolog.DebugLevel).With().Timestamp().Logger()
	logger := zerologr.New(&zeroLogger)

	var err error
	otelShutdown, err = instrumentation.SetupOTelSDK(ctx, "drivers-tests", "", logger)
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	if otelShutdown != nil {
		_ = otelShutdown(context.Background())
	}
})

var _ = Describe("Driver Image Selection", func() {

	Describe("GetBuilderImageForNode", func() {
		It("should return ubuntu24 builder image for Ubuntu 24.04 nodes", func() {
			node := &corev1.Node{
				Status: corev1.NodeStatus{
					NodeInfo: corev1.NodeSystemInfo{
						OSImage: "Ubuntu 24.04.3 LTS",
					},
				},
			}

			image := GetBuilderImageForNode(node)

			Expect(image).To(Equal("quay.io/weka.io/weka-drivers-build-images:builder-ubuntu24"))
		})

		It("should return ubuntu22 builder image for non-Ubuntu 24.04 nodes", func() {
			testCases := []string{
				"Ubuntu 22.04.5 LTS",
				"Rocky Linux 8.10",
				"RHEL 9.4",
				"Debian GNU/Linux 12 (bookworm)",
			}

			for _, osImage := range testCases {
				node := &corev1.Node{
					Status: corev1.NodeStatus{
						NodeInfo: corev1.NodeSystemInfo{
							OSImage: osImage,
						},
					},
				}

				image := GetBuilderImageForNode(node)

				Expect(image).To(Equal("quay.io/weka.io/weka-drivers-build-images:builder-ubuntu22"),
					"Expected ubuntu22 builder for OS: %s", osImage)
			}
		})
	})

	Describe("GetLoaderImageForNode", func() {
		var ctx context.Context

		BeforeEach(func() {
			ctx = context.Background()
		})

		It("should return the cluster image when feature flag WekaGetCopyLocalDriverFiles is true", func() {
			clusterImage := "quay.io/weka.io/weka-in-container:4.5.0.100"
			node := &corev1.Node{
				Status: corev1.NodeStatus{
					NodeInfo: corev1.NodeSystemInfo{
						OSImage: "Ubuntu 22.04.5 LTS",
					},
				},
			}

			// Pre-populate the feature flags cache with the flag enabled
			flags := &domain.FeatureFlags{
				WekaGetCopyLocalDriverFiles: true,
			}
			err := services.SetFeatureFlags(ctx, clusterImage, flags)
			Expect(err).NotTo(HaveOccurred())

			loaderImage := GetLoaderImageForNode(ctx, node, clusterImage)

			Expect(loaderImage).To(Equal(clusterImage))
		})

		It("should return builder image when feature flag is not set or flags not cached", func() {
			// Use an image that is not in the cache
			clusterImage := "quay.io/weka.io/weka-in-container:4.4.0.50-uncached"
			node := &corev1.Node{
				Status: corev1.NodeStatus{
					NodeInfo: corev1.NodeSystemInfo{
						OSImage: "Ubuntu 22.04.5 LTS",
					},
				},
			}

			loaderImage := GetLoaderImageForNode(ctx, node, clusterImage)

			// Should fall back to builder image since flags are not cached
			Expect(loaderImage).To(Equal("quay.io/weka.io/weka-drivers-build-images:builder-ubuntu22"))
		})
	})
})
