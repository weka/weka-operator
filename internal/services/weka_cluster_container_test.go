package services

import (
	"encoding/json"
	"testing"
)

// TestWekaClusterContainerDecode pins the decode of `weka cluster container <id> --json` against a
// verbatim payload from a live cluster. applyCurrentImage's cluster-side gate reads state, status and
// the two version fields off this struct, and the CLI emits many more members than we decode - so
// what matters here is that the fields we depend on land, and that extra members do not break the
// unmarshal.
func TestWekaClusterContainerDecode(t *testing.T) {
	// Trimmed to the members around the ones we read, keeping their exact shapes.
	payload := `[
	{
		"added_time": "2026-08-18T12:08:58.276769Z",
		"auto_remove_timeout": null,
		"container_name": "drivexda7e5872xa57ax4d2bxb7fdx3f5237ae2202",
		"cores": 1,
		"cores_ids": [1],
		"failure_text": "Stop requested (1 hour ago)",
		"host_id": "HostId<4>",
		"host_ip": "10.100.5.49",
		"hostname": "h1-3-a",
		"ips": ["10.100.5.49"],
		"memory": 1631584256,
		"mode": "backend",
		"os_info": {"kernel_name": "Linux", "platform": "x86_64"},
		"state": "ACTIVE",
		"status": "UP",
		"sw_release_string": "5.1.31",
		"sw_version": "5.1.31",
		"uid": "acc0de81-0061-5a3f-47b5-7c372264c524"
	}
]`

	var containers []WekaClusterContainer
	if err := json.Unmarshal([]byte(payload), &containers); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(containers) != 1 {
		t.Fatalf("got %d containers, want 1", len(containers))
	}

	c := containers[0]
	for _, f := range []struct {
		name, got, want string
	}{
		{"HostId", c.HostId, "HostId<4>"},
		{"HostIp", c.HostIp, "10.100.5.49"},
		{"Hostname", c.Hostname, "h1-3-a"},
		{"ContainerName", c.ContainerName, "drivexda7e5872xa57ax4d2bxb7fdx3f5237ae2202"},
		{"Uid", c.Uid, "acc0de81-0061-5a3f-47b5-7c372264c524"},
		{"State", c.State, "ACTIVE"},
		{"Status", c.Status, "UP"},
		{"SwVersion", c.SwVersion, "5.1.31"},
		{"SwReleaseString", c.SwReleaseString, "5.1.31"},
	} {
		if f.got != f.want {
			t.Errorf("%s = %q, want %q", f.name, f.got, f.want)
		}
	}
}

// TestWekaClusterContainerReportedVersion covers the sw_release_string / sw_version split: custom and
// feature builds carry the build suffix only in the release string, so that is the one the image tag
// has to be compared against. When the two agree there is no suffix and either will do; when weka
// reports neither, the caller must be able to tell (empty) and skip the comparison rather than
// blocking an upgrade on a version it never learned.
func TestWekaClusterContainerReportedVersion(t *testing.T) {
	cases := []struct {
		name          string
		swVersion     string
		releaseString string
		want          string
	}{
		{
			name:          "plain release, both fields agree",
			swVersion:     "5.1.31",
			releaseString: "5.1.31",
			want:          "5.1.31",
		},
		{
			name:          "custom build, suffix only in release string",
			swVersion:     "1.2.3.4",
			releaseString: "1.2.3.4-custom-build",
			want:          "1.2.3.4-custom-build",
		},
		{
			name:      "release string absent, fall back to sw_version",
			swVersion: "5.1.31",
			want:      "5.1.31",
		},
		{
			name: "neither reported",
			want: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cc := WekaClusterContainer{SwVersion: c.swVersion, SwReleaseString: c.releaseString}
			if got := cc.ReportedVersion(); got != c.want {
				t.Errorf("ReportedVersion() = %q, want %q", got, c.want)
			}
		})
	}
}
