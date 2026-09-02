package drivers

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/weka/go-weka-observability/instrumentation"
	obslogger "github.com/weka/go-weka-observability/logger"
	corev1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/config"
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
	logger := obslogger.CreateLogger(obslogger.WithConsoleSink(), obslogger.WithDebugLevel())

	var err error
	otelShutdown, err = instrumentation.SetupOTelSDKWithOptions(ctx, "drivers-tests", "", logger)
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	if otelShutdown != nil {
		_ = otelShutdown(context.Background())
	}
})

func TestNormalizeOSImageName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"Ubuntu 22.04.5 LTS", "Ubuntu 22.04.5 LTS", "ubuntu-22-04"},
		{"Ubuntu 24.04.3 LTS", "Ubuntu 24.04.3 LTS", "ubuntu-24"},
		{"Ubuntu 24.04.4 LTS", "Ubuntu 24.04.4 LTS", "ubuntu-24"},
		{"RHEL 9.4", "RHEL 9.4", "rhel09"},
		{"Red Hat Enterprise Linux 9.7 (Plow)", "Red Hat Enterprise Linux 9.7 (Plow)", "rhel09"},
		{"Red Hat Enterprise Linux 8.10", "Red Hat Enterprise Linux 8.10", "rhel08"},
		{"Rocky Linux 8.10", "Rocky Linux 8.10", "rocky08"},
		{"empty string", "", "unknown-os"},
		{"Some Weird Distro 1.2", "Some Weird Distro 1.2", "unknown-os"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeOSImageName(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeOSImageName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

var _ = Describe("Driver Image Selection", func() {

	BeforeEach(func() {
		config.Config.BuilderImages.Default = "quay.io/weka.io/weka-drivers-build-images:builder-ubuntu22"
		config.Config.BuilderImages.Ubuntu24 = "quay.io/weka.io/weka-drivers-build-images:builder-ubuntu24"
	})

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
