package resources

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// TestShouldRequestNICs verifies that setResources only requests the
// weka.io/weka-nics extended resource when the container's network spec does
// NOT pin explicit data-path devices (selectors/deviceSubnets/ethDevice(s)).
// This mirrors GetNetDevices' precedence chain (see utils.HasExplicitNetDevices):
// explicit devices short-circuit before the VF-per-IO-node branch, so no NICs
// should be requested in that case even on a supported cloud provider.
func TestShouldRequestNICs(t *testing.T) {
	cases := []struct {
		name     string
		provider discovery.Provider
		mode     string
		network  weka.Network
		wantNICs bool
	}{
		{
			name:     "no explicit devices, supported provider -> request NICs",
			provider: discovery.ProviderAWS,
			mode:     weka.WekaContainerModeCompute,
			network:  weka.Network{},
			wantNICs: true,
		},
		{
			name:     "device subnets set -> no NICs requested",
			provider: discovery.ProviderAWS,
			mode:     weka.WekaContainerModeCompute,
			network:  weka.Network{DeviceSubnets: []string{"10.0.0.0/24"}},
			wantNICs: false,
		},
		{
			name:     "selectors set -> no NICs requested",
			provider: discovery.ProviderAWS,
			mode:     weka.WekaContainerModeCompute,
			network:  weka.Network{Selectors: []weka.NetworkSelector{{}}},
			wantNICs: false,
		},
		{
			name:     "eth device set -> no NICs requested",
			provider: discovery.ProviderAWS,
			mode:     weka.WekaContainerModeCompute,
			network:  weka.Network{EthDevice: "eth0"},
			wantNICs: false,
		},
		{
			name:     "eth devices set -> no NICs requested",
			provider: discovery.ProviderAWS,
			mode:     weka.WekaContainerModeCompute,
			network:  weka.Network{EthDevices: []string{"eth0"}},
			wantNICs: false,
		},
		{
			name:     "udp mode, no explicit devices -> no NICs requested",
			provider: discovery.ProviderAWS,
			mode:     weka.WekaContainerModeCompute,
			network:  weka.Network{UdpMode: true},
			wantNICs: false,
		},
		// OCI cases: on OKE the VF path defaults ON (provider prefix match, see
		// discovery.IsSupportedCloudProvider), so a config with no explicit devices
		// requests NICs and deadlocks unless an ensure-nics WekaPolicy has published
		// weka.io/weka-nics node capacity. Any explicit device setting must opt out.
		{
			name:     "oci, no explicit devices -> request NICs",
			provider: discovery.ProviderOCI,
			mode:     weka.WekaContainerModeCompute,
			network:  weka.Network{},
			wantNICs: true,
		},
		{
			name:     "oci, device subnets set -> no NICs requested",
			provider: discovery.ProviderOCI,
			mode:     weka.WekaContainerModeCompute,
			network:  weka.Network{DeviceSubnets: []string{"10.0.0.0/16"}},
			wantNICs: false,
		},
		{
			name:     "oci, selectors set -> no NICs requested",
			provider: discovery.ProviderOCI,
			mode:     weka.WekaContainerModeCompute,
			network:  weka.Network{Selectors: []weka.NetworkSelector{{}}},
			wantNICs: false,
		},
		{
			name:     "oci, eth device set -> no NICs requested",
			provider: discovery.ProviderOCI,
			mode:     weka.WekaContainerModeCompute,
			network:  weka.Network{EthDevice: "eth0"},
			wantNICs: false,
		},
		{
			name:     "oci, eth devices set -> no NICs requested",
			provider: discovery.ProviderOCI,
			mode:     weka.WekaContainerModeCompute,
			network:  weka.Network{EthDevices: []string{"eth0"}},
			wantNICs: false,
		},
		{
			// Control: with no explicit AllocateVfPerIoNode override and no
			// explicit devices, an unsupported provider falls through to
			// nodeInfo.HasSupportedCloudProvider() == false, so no NICs are
			// requested. This proves the AWS/OCI "-> request NICs" cases above
			// pass because of the provider, not despite it.
			name:     "unsupported provider, no explicit devices -> no NICs requested",
			provider: discovery.ProviderUnknown,
			mode:     weka.WekaContainerModeCompute,
			network:  weka.Network{},
			wantNICs: false,
		},
		{
			// AllocateVfPerIoNode: true explicitly overrides the provider default,
			// so NICs are requested even on an unsupported provider.
			name:     "allocateVfPerIoNode true, no explicit devices, unsupported provider -> request NICs",
			provider: discovery.ProviderUnknown,
			mode:     weka.WekaContainerModeCompute,
			network:  weka.Network{AllocateVfPerIoNode: ptr(true)},
			wantNICs: true,
		},
		{
			// AllocateVfPerIoNode: false explicitly overrides the provider default,
			// so NICs are NOT requested even on a supported provider.
			name:     "allocateVfPerIoNode false, no explicit devices, supported provider -> no NICs requested",
			provider: discovery.ProviderAWS,
			mode:     weka.WekaContainerModeCompute,
			network:  weka.Network{AllocateVfPerIoNode: ptr(false)},
			wantNICs: false,
		},
		{
			// Explicit devices win over an explicit AllocateVfPerIoNode: true opt-in.
			// This mirrors GetNetDevices' precedence (utils.HasExplicitNetDevices short-
			// circuits ahead of the VF-per-IO-node branch): a config that pins devices
			// AND explicitly asks for VF-per-IO-node allocation still gets no NICs
			// requested, because the explicit-device path is used for network setup
			// instead. This is correct per that precedence but easy to get surprised
			// by, hence pinning it here.
			name:     "allocateVfPerIoNode true + explicit device -> no NICs requested (explicit device wins)",
			provider: discovery.ProviderAWS,
			mode:     weka.WekaContainerModeCompute,
			network: weka.Network{
				AllocateVfPerIoNode: ptr(true),
				DeviceSubnets:       []string{"10.0.0.0/24"},
			},
			wantNICs: false,
		},
		// Non-cluster-joining container kinds: these never consume per-IO-node VFs
		// (ShouldJoinCluster()==false), so even on a supported provider with no
		// explicit devices they must not request weka.io/weka-nics. Before the
		// pod.go gate was changed from !IsDriversContainer() to ShouldJoinCluster(),
		// these all incorrectly requested NICs.
		{
			name:     "discovery mode, supported provider, no explicit devices -> no NICs requested",
			provider: discovery.ProviderAWS,
			mode:     weka.WekaContainerModeDiscovery,
			network:  weka.Network{},
			wantNICs: false,
		},
		{
			name:     "envoy mode, supported provider, no explicit devices -> no NICs requested",
			provider: discovery.ProviderAWS,
			mode:     weka.WekaContainerModeEnvoy,
			network:  weka.Network{},
			wantNICs: false,
		},
		{
			name:     "ssdproxy mode, supported provider, no explicit devices -> no NICs requested",
			provider: discovery.ProviderAWS,
			mode:     weka.WekaContainerModeSSDProxy,
			network:  weka.Network{},
			wantNICs: false,
		},
		{
			name:     "telemetry mode, supported provider, no explicit devices -> no NICs requested",
			provider: discovery.ProviderAWS,
			mode:     weka.WekaContainerModeTelemetry,
			network:  weka.Network{},
			wantNICs: false,
		},
		{
			name:     "adhoc-op-with-container mode, supported provider, no explicit devices -> no NICs requested",
			provider: discovery.ProviderAWS,
			mode:     weka.WekaContainerModeAdhocOpWC,
			network:  weka.Network{},
			wantNICs: false,
		},
	}

	hgDetails := minimalHgDetails()

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			nodeInfo := &discovery.DiscoveryNodeInfo{
				Provider: tc.provider,
			}
			factory := makeFactory(2, weka.CpuPolicyDedicatedHT, nodeInfo)
			factory.container.Spec.Mode = tc.mode
			factory.container.Spec.Network = tc.network
			pod := makePod(weka.CpuPolicyDedicatedHT)

			if err := factory.setResources(context.Background(), pod, hgDetails); err != nil {
				t.Fatalf("setResources returned unexpected error: %v", err)
			}

			gotQty, gotNICs := pod.Spec.Containers[0].Resources.Requests[domain.WEKANICs]
			if gotNICs != tc.wantNICs {
				t.Errorf("weka-nics requested = %v, want %v", gotNICs, tc.wantNICs)
			}
			if tc.wantNICs {
				if gotQty.Value() != int64(factory.container.Spec.NumCores) {
					t.Errorf("weka-nics request = %d, want %d", gotQty.Value(), factory.container.Spec.NumCores)
				}
				limitQty := pod.Spec.Containers[0].Resources.Limits[domain.WEKANICs]
				if limitQty.Cmp(gotQty) != 0 {
					t.Errorf("weka-nics limit %v != request %v", &limitQty, &gotQty)
				}
			}
		})
	}
}
