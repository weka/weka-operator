package services

import "testing"

func TestParseWekaStatusUpgradePhase(t *testing.T) {
	const legacyHead = `{"status":"OK","stripe_data_drives":4,"stripe_protection_drives":2`
	const wekactlHead = `{"status":"OK","stripe":{"data":4,"protection":2,"hotspare":1}`

	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"legacy in phase", legacyHead + `,"upgrade_info":{"upgrade_phase":"DRIVES_UPGRADE","upgraded_containers":3}}`, UpgradePhaseDrives},
		{"legacy data services", legacyHead + `,"upgrade_info":{"upgrade_phase":"DATA_SERVICE_UPGRADE"}}`, UpgradePhaseDataServices},
		{"legacy idle empty phase", legacyHead + `,"upgrade_info":{"upgrade_phase":""}}`, ""},
		{"legacy idle null upgrade_info", legacyHead + `,"upgrade_info":null}`, ""},
		{"legacy upgrade_info absent", legacyHead + `}`, ""},
		{"wekactl in phase", wekactlHead + `,"upgrade":{"phase":"COMPUTE_UPGRADE","type":"AGENT_DRIVEN_UPGRADE"}}`, UpgradePhaseCompute},
		{"wekactl frontend", wekactlHead + `,"upgrade":{"phase":"FRONTEND_UPGRADE"}}`, UpgradePhaseFrontend},
		{"wekactl idle NONE", wekactlHead + `,"upgrade":{"phase":"NONE"}}`, "NONE"},
		{"wekactl upgrade null", wekactlHead + `,"upgrade":null}`, ""},
		{"wekactl upgrade absent", wekactlHead + `}`, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, err := parseWekaStatus([]byte(c.payload))
			if err != nil {
				t.Fatalf("parseWekaStatus: %v", err)
			}
			if got := status.UpgradeInfo.UpgradePhase; got != c.want {
				t.Errorf("upgrade phase = %q, want %q", got, c.want)
			}
		})
	}
}
