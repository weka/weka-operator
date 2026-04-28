package config

import (
	"testing"
)

func TestFeatureFlagsFromBitmap(t *testing.T) {
	tests := []struct {
		name   string
		bitmap string
		// expected field values
		tracesOverridePartial      bool
		tracesOverrideSlash        bool
		supportsBindingNotAll       bool
		agentValidate60Ports       bool
		allowPerContainerDrivers   bool
		wekaGetCopyLocalDrivers    bool
		driverSupportsAutoDrain    bool
		ssdProxyIommuSupport       bool
		ssdProxyIncludesDpdk       bool
	}{
		{
			name:   "invalid base64 returns all false",
			bitmap: "not-valid-base64!!!",
		},
		{
			name:   "all-zero byte returns all false",
			bitmap: "AA==", // 0x00
		},
		{
			name:                  "bit 0 sets TracesOverridePartialSupport only",
			bitmap:                "AQ==", // 0x01
			tracesOverridePartial: true,
		},
		{
			name:                "bit 1 sets TracesOverrideInSlashTraces only",
			bitmap:              "Ag==", // 0x02
			tracesOverrideSlash: true,
		},
		{
			name:                 "bit 2 sets SupportsBindingToNotAllInterfaces only",
			bitmap:               "BA==", // 0x04
			supportsBindingNotAll: true,
		},
		{
			name:                "bit 7 sets SsdProxyIommuSupport only",
			bitmap:              "gA==", // 0x80
			ssdProxyIommuSupport: true,
		},
		{
			name:   "bit 8 is unused — maps to nothing",
			bitmap: "AAE=", // byte[0]=0x00, byte[1]=0x01 → bit 8 set
			// all flags remain false: bit 8 is explicitly unused
		},
		{
			name:             "bit 9 sets SsdProxyIncludesDpdkMemory only",
			bitmap:           "AAI=", // byte[0]=0x00, byte[1]=0x02 → bit 9 set
			ssdProxyIncludesDpdk: true,
		},
		{
			// "Bw==" from 4.4.10 release: 0x07 = bits 0,1,2
			name:                  "real bitmap Bw== from 4.4.10",
			bitmap:                "Bw==",
			tracesOverridePartial: true,
			tracesOverrideSlash:   true,
			supportsBindingNotAll:  true,
		},
		{
			// "Hw==" from 5.1.0 release: 0x1F = bits 0,1,2,3,4
			name:                   "real bitmap Hw== from 5.1.0",
			bitmap:                 "Hw==",
			tracesOverridePartial:  true,
			tracesOverrideSlash:    true,
			supportsBindingNotAll:  true,
			agentValidate60Ports:   true,
			allowPerContainerDrivers: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := featureFlagsFromBitmap(tt.bitmap)

			check := func(field string, got, want bool) {
				t.Helper()
				if got != want {
					t.Errorf("%s: got %v, want %v", field, got, want)
				}
			}

			check("TracesOverridePartialSupport", got.TracesOverridePartialSupport, tt.tracesOverridePartial)
			check("TracesOverrideInSlashTraces", got.TracesOverrideInSlashTraces, tt.tracesOverrideSlash)
			check("SupportsBindingToNotAllInterfaces", got.SupportsBindingToNotAllInterfaces, tt.supportsBindingNotAll)
			check("AgentValidate60PortsPerContainer", got.AgentValidate60PortsPerContainer, tt.agentValidate60Ports)
			check("AllowPerContainerDriverInterfaces", got.AllowPerContainerDriverInterfaces, tt.allowPerContainerDrivers)
			check("WekaGetCopyLocalDriverFiles", got.WekaGetCopyLocalDriverFiles, tt.wekaGetCopyLocalDrivers)
			check("DriverSupportsAutoDrain", got.DriverSupportsAutoDrain, tt.driverSupportsAutoDrain)
			check("SsdProxyIommuSupport", got.SsdProxyIommuSupport, tt.ssdProxyIommuSupport)
			check("SsdProxyIncludesDpdkMemory", got.SsdProxyIncludesDpdkMemory, tt.ssdProxyIncludesDpdk)
		})
	}
}
