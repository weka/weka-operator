package drivers

import (
	"context"
	"encoding/json"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/weka/go-weka-observability/instrumentation"
	obslogger "github.com/weka/go-weka-observability/logger"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
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
		{"NixOS 26.05 (Yarara)", "NixOS 26.05 (Yarara)", "nixos-26-05"},
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

// nodeWithDiscoveryInfo returns a node whose weka.io/discovery.json annotation carries info,
// mirroring what the discovery container writes and DiscoverNodeOperation stamps onto the node.
func nodeWithDiscoveryInfo(osImage string, info *discovery.DiscoveryNodeInfo) *corev1.Node {
	node := &corev1.Node{
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				OSImage: osImage,
			},
		},
	}
	if info != nil {
		raw, err := json.Marshal(info)
		Expect(err).NotTo(HaveOccurred())
		node.ObjectMeta = metav1.ObjectMeta{
			Annotations: map[string]string{discovery.DiscoveryAnnotation: string(raw)},
		}
	}
	return node
}

var _ = Describe("Driver Image Selection", func() {

	BeforeEach(func() {
		config.Config.BuilderImages.Default = "quay.io/weka.io/weka-drivers-build-images:builder-ubuntu22"
		config.Config.BuilderImages.Ubuntu24 = "quay.io/weka.io/weka-drivers-build-images:builder-ubuntu24"
		config.Config.BuilderImages.Nixos = map[string]string{
			"nixos-gcc15": "quay.io/weka.io/weka-drivers-build-images:builder-nixos-gcc15-v2",
		}
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

			image, err := GetBuilderImageForNode(node)

			Expect(err).NotTo(HaveOccurred())
			Expect(image).To(Equal("quay.io/weka.io/weka-drivers-build-images:builder-ubuntu24"))
		})

		It("should return the gcc-matched nixos builder image for a classified NixOS node", func() {
			node := nodeWithDiscoveryInfo("NixOS 26.05 (Yarara)", &discovery.DiscoveryNodeInfo{
				Os:          "nixos-gcc15",
				ProcVersion: "Linux version 6.18.52 (nixbld@localhost) (gcc (GCC) 15.2.0, ...)",
			})

			image, err := GetBuilderImageForNode(node)

			Expect(err).NotTo(HaveOccurred())
			Expect(image).To(Equal("quay.io/weka.io/weka-drivers-build-images:builder-nixos-gcc15-v2"))
		})

		It("should error for a NixOS node whose gcc major has no configured builder image", func() {
			node := nodeWithDiscoveryInfo("NixOS 26.05 (Yarara)", &discovery.DiscoveryNodeInfo{
				Os:          "nixos-gcc14",
				ProcVersion: "Linux version 6.18.52 (nixbld@localhost) (gcc (GCC) 14.2.0, ...)",
			})

			_, err := GetBuilderImageForNode(node)

			Expect(err).To(HaveOccurred())
		})

		It("should error for a NixOS node with no discovery annotation yet", func() {
			node := nodeWithDiscoveryInfo("NixOS 26.05 (Yarara)", nil)

			_, err := GetBuilderImageForNode(node)

			Expect(err).To(HaveOccurred())
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

				image, err := GetBuilderImageForNode(node)

				Expect(err).NotTo(HaveOccurred())
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

			loaderImage, err := GetLoaderImageForNode(ctx, node, clusterImage, false)

			Expect(err).NotTo(HaveOccurred())
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

			loaderImage, err := GetLoaderImageForNode(ctx, node, clusterImage, false)

			// Should fall back to builder image since flags are not cached
			Expect(err).NotTo(HaveOccurred())
			Expect(loaderImage).To(Equal("quay.io/weka.io/weka-drivers-build-images:builder-ubuntu22"))
		})

		It("should return the builder image when forceBuilderCli is set, even with the flag on", func() {
			clusterImage := "quay.io/weka.io/weka-in-container:4.5.0.101"
			node := &corev1.Node{
				Status: corev1.NodeStatus{
					NodeInfo: corev1.NodeSystemInfo{
						OSImage: "Ubuntu 22.04.5 LTS",
					},
				},
			}

			flags := &domain.FeatureFlags{
				WekaGetCopyLocalDriverFiles: true,
			}
			err := services.SetFeatureFlags(ctx, clusterImage, flags)
			Expect(err).NotTo(HaveOccurred())

			loaderImage, err := GetLoaderImageForNode(ctx, node, clusterImage, true)

			Expect(err).NotTo(HaveOccurred())
			Expect(loaderImage).To(Equal("quay.io/weka.io/weka-drivers-build-images:builder-ubuntu22"),
				"forceBuilderCli must override the feature flag")
		})

		It("should return the builder image when forceBuilderCli is set and flags are not cached", func() {
			clusterImage := "quay.io/weka.io/weka-in-container:4.4.0.51-uncached"
			node := &corev1.Node{
				Status: corev1.NodeStatus{
					NodeInfo: corev1.NodeSystemInfo{
						OSImage: "Ubuntu 24.04.3 LTS",
					},
				},
			}

			loaderImage, err := GetLoaderImageForNode(ctx, node, clusterImage, true)

			Expect(err).NotTo(HaveOccurred())
			Expect(loaderImage).To(Equal("quay.io/weka.io/weka-drivers-build-images:builder-ubuntu24"))
		})
	})
})
