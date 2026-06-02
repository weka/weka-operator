package wekadrive

import (
	"reflect"
	"testing"

	"github.com/weka/weka-operator/internal/pkg/domain"
)

// ---------------------------------------------------------------------------
// buildSignFlags
// ---------------------------------------------------------------------------

func TestBuildSignFlags(t *testing.T) {
	tests := []struct {
		name string
		opts *SignOptions
		want []string
	}{
		{
			name: "nil opts returns nil",
			opts: nil,
			want: nil,
		},
		{
			name: "all false returns empty (non-nil) via no appends",
			opts: &SignOptions{},
			want: nil,
		},
		{
			name: "AllowEraseWekaPartitions only",
			opts: &SignOptions{AllowEraseWekaPartitions: true},
			want: []string{"--allow-erase-weka-partitions"},
		},
		{
			name: "AllowEraseNonWekaPartitions only",
			opts: &SignOptions{AllowEraseNonWekaPartitions: true},
			want: []string{"--allow-erase-non-weka-partitions"},
		},
		{
			name: "AllowNonEmptyDevice only",
			opts: &SignOptions{AllowNonEmptyDevice: true},
			want: []string{"--allow-non-empty-device"},
		},
		{
			name: "SkipTrimFormat only",
			opts: &SignOptions{SkipTrimFormat: true},
			want: []string{"--skip-trim-format"},
		},
		{
			name: "all four true — declared order",
			opts: &SignOptions{
				AllowEraseWekaPartitions:    true,
				AllowEraseNonWekaPartitions: true,
				AllowNonEmptyDevice:         true,
				SkipTrimFormat:              true,
			},
			want: []string{
				"--allow-erase-weka-partitions",
				"--allow-erase-non-weka-partitions",
				"--allow-non-empty-device",
				"--skip-trim-format",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSignFlags(tt.opts)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildSignFlags(%+v) = %v; want %v", tt.opts, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// iuSizeToDriveType
// ---------------------------------------------------------------------------

func TestIuSizeToDriveType(t *testing.T) {
	tests := []struct {
		name   string
		iuSize int
		want   string
	}{
		{name: "zero -> TLC", iuSize: 0, want: "TLC"},
		{name: "4096 -> TLC", iuSize: 4096, want: "TLC"},
		{name: "16383 boundary-1 -> TLC", iuSize: 16383, want: "TLC"},
		{name: "16384 boundary -> QLC", iuSize: 16384, want: "QLC"},
		{name: "32768 above -> QLC", iuSize: 32768, want: "QLC"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := iuSizeToDriveType(tt.iuSize)
			if got != tt.want {
				t.Errorf("iuSizeToDriveType(%d) = %q; want %q", tt.iuSize, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// filterClusterGUIDDrives
// ---------------------------------------------------------------------------

// realSignDriveListJSON is representative of actual `weka-sign-drive list -j` output:
// 6 weka_formatted drives with cluster_guid=null, is_proxy=false, hardware has no "path"
// key (only top-level "path"), plus 2 excluded drives with weka_info=null.
const realSignDriveListJSON = `{
  "devices": [
    {
      "path": "/dev/nvme0n1",
      "status": "weka_formatted",
      "physical_uuid": "55943511-a49e-4dec-ba9a-20d084917627",
      "weka_info": {
        "cluster_guid": null,
        "is_proxy": false
      },
      "hardware": {
        "serial_number": "22184A1936FE",
        "size_bytes": 7681501126656,
        "iu_size": 4096
      }
    },
    {
      "path": "/dev/nvme1n1",
      "status": "weka_formatted",
      "physical_uuid": "66a12345-b59f-4dec-bb9a-31e095028738",
      "weka_info": {
        "cluster_guid": null,
        "is_proxy": false
      },
      "hardware": {
        "serial_number": "22184A1936FF",
        "size_bytes": 7681501126656,
        "iu_size": 4096
      }
    },
    {
      "path": "/dev/nvme2n1",
      "status": "weka_formatted",
      "physical_uuid": "77b23456-c60g-4dec-cc9a-42f106139849",
      "weka_info": {
        "cluster_guid": null,
        "is_proxy": false
      },
      "hardware": {
        "serial_number": "22184A1937AA",
        "size_bytes": 7681501126656,
        "iu_size": 4096
      }
    },
    {
      "path": "/dev/nvme3n1",
      "status": "weka_formatted",
      "physical_uuid": "88c34567-d71h-4dec-dd9a-53g21724095a",
      "weka_info": {
        "cluster_guid": null,
        "is_proxy": false
      },
      "hardware": {
        "serial_number": "22184A1937BB",
        "size_bytes": 7681501126656,
        "iu_size": 4096
      }
    },
    {
      "path": "/dev/nvme4n1",
      "status": "weka_formatted",
      "physical_uuid": "99d45678-e82i-4dec-ee9a-64h3283840ab",
      "weka_info": {
        "cluster_guid": null,
        "is_proxy": false
      },
      "hardware": {
        "serial_number": "22184A1937CC",
        "size_bytes": 7681501126656,
        "iu_size": 4096
      }
    },
    {
      "path": "/dev/nvme5n1",
      "status": "weka_formatted",
      "physical_uuid": "aae56789-f93j-4dec-ff9a-75i439495bc",
      "weka_info": {
        "cluster_guid": null,
        "is_proxy": false
      },
      "hardware": {
        "serial_number": "22184A1937DD",
        "size_bytes": 7681501126656,
        "iu_size": 4096
      }
    },
    {
      "path": "/dev/nvme6n1",
      "status": "excluded",
      "physical_uuid": "",
      "weka_info": null,
      "hardware": {
        "serial_number": "22184A1937EE",
        "size_bytes": 7681501126656,
        "iu_size": 4096
      }
    },
    {
      "path": "/dev/nvme7n1",
      "status": "excluded",
      "physical_uuid": "",
      "weka_info": null,
      "hardware": {
        "serial_number": "22184A1937FF",
        "size_bytes": 7681501126656,
        "iu_size": 4096
      }
    }
  ]
}`

func mustParseSignDriveList(t *testing.T, jsonStr string) signDriveListOutput {
	t.Helper()
	var out signDriveListOutput
	if err := parseSignDriveListJSON([]byte(jsonStr), &out); err != nil {
		t.Fatalf("JSON parse failed: %v", err)
	}
	return out
}

func TestFilterClusterGUIDDrives(t *testing.T) {
	t.Run("real fixture — no cluster_guid — empty map", func(t *testing.T) {
		parsed := mustParseSignDriveList(t, realSignDriveListJSON)
		got := filterClusterGUIDDrives(parsed)
		if len(got) != 0 {
			t.Errorf("expected empty map, got %v", got)
		}
		// must not panic on weka_info=null devices (the excluded ones)
	})

	t.Run("device with cluster_guid — top-level path used as fallback", func(t *testing.T) {
		// hardware has no "path" key; only top-level path should be used.
		const json = `{
      "devices": [
        {
          "path": "/dev/nvmeXn1",
          "status": "weka_formatted",
          "physical_uuid": "some-uuid",
          "weka_info": { "cluster_guid": "some-guid-123", "is_proxy": false },
          "hardware": { "serial_number": "SER1", "size_bytes": 1000000000, "iu_size": 4096 }
        }
      ]
    }`
		parsed := mustParseSignDriveList(t, json)
		got := filterClusterGUIDDrives(parsed)
		want := map[string]string{"SER1": "/dev/nvmeXn1"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v; want %v", got, want)
		}
	})

	t.Run("device with cluster_guid — hardware path takes priority over top-level path", func(t *testing.T) {
		const json = `{
      "devices": [
        {
          "path": "/dev/nvmeXn1",
          "status": "weka_formatted",
          "physical_uuid": "some-uuid",
          "weka_info": { "cluster_guid": "some-guid-123", "is_proxy": false },
          "hardware": { "serial_number": "SER2", "path": "/dev/hwpath", "size_bytes": 1000000000, "iu_size": 4096 }
        }
      ]
    }`
		parsed := mustParseSignDriveList(t, json)
		got := filterClusterGUIDDrives(parsed)
		want := map[string]string{"SER2": "/dev/hwpath"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v; want %v", got, want)
		}
	})

	t.Run("empty serial — skipped", func(t *testing.T) {
		const json = `{
      "devices": [
        {
          "path": "/dev/nvmeXn1",
          "status": "weka_formatted",
          "physical_uuid": "some-uuid",
          "weka_info": { "cluster_guid": "some-guid-123", "is_proxy": false },
          "hardware": { "serial_number": "", "size_bytes": 1000000000, "iu_size": 4096 }
        }
      ]
    }`
		parsed := mustParseSignDriveList(t, json)
		got := filterClusterGUIDDrives(parsed)
		if len(got) != 0 {
			t.Errorf("expected empty map for missing serial, got %v", got)
		}
	})

	t.Run("weka_info nil — skipped", func(t *testing.T) {
		const json = `{
      "devices": [
        {
          "path": "/dev/nvmeXn1",
          "status": "excluded",
          "physical_uuid": "",
          "weka_info": null,
          "hardware": { "serial_number": "SER3", "size_bytes": 1000000000, "iu_size": 4096 }
        }
      ]
    }`
		parsed := mustParseSignDriveList(t, json)
		got := filterClusterGUIDDrives(parsed)
		if len(got) != 0 {
			t.Errorf("expected empty map for nil weka_info, got %v", got)
		}
	})
}

// ---------------------------------------------------------------------------
// extractProxyDrives
// ---------------------------------------------------------------------------

func TestExtractProxyDrives(t *testing.T) {
	t.Run("real fixture — no proxy drives — empty slice", func(t *testing.T) {
		parsed := mustParseSignDriveList(t, realSignDriveListJSON)
		got := extractProxyDrives(parsed)
		if len(got) != 0 {
			t.Errorf("expected empty slice, got %v", got)
		}
		// must not panic on weka_info=null devices
	})

	t.Run("is_proxy=true TLC (iu_size=4096)", func(t *testing.T) {
		const json = `{
      "devices": [
        {
          "path": "/dev/nvmeXn1",
          "status": "weka_formatted",
          "physical_uuid": "uuid-1",
          "weka_info": { "cluster_guid": "some-guid", "is_proxy": true },
          "hardware": {
            "serial_number": "SERP1",
            "size_bytes": 17179869184,
            "iu_size": 4096
          }
        }
      ]
    }`
		// 17179869184 = 16 * 1024 * 1024 * 1024 = 16 GiB
		parsed := mustParseSignDriveList(t, json)
		got := extractProxyDrives(parsed)
		if len(got) != 1 {
			t.Fatalf("expected 1 drive, got %d: %v", len(got), got)
		}
		want := domain.SharedDriveInfo{
			PhysicalUUID: "uuid-1",
			Serial:       "SERP1",
			CapacityGiB:  16,
			Type:         "TLC",
		}
		if got[0] != want {
			t.Errorf("got %+v; want %+v", got[0], want)
		}
	})

	t.Run("is_proxy=true QLC (iu_size=16384)", func(t *testing.T) {
		const json = `{
      "devices": [
        {
          "path": "/dev/nvmeQn1",
          "status": "weka_formatted",
          "physical_uuid": "uuid-qlc",
          "weka_info": { "cluster_guid": "some-guid", "is_proxy": true },
          "hardware": {
            "serial_number": "SERQLC",
            "size_bytes": 17179869184,
            "iu_size": 16384
          }
        }
      ]
    }`
		parsed := mustParseSignDriveList(t, json)
		got := extractProxyDrives(parsed)
		if len(got) != 1 {
			t.Fatalf("expected 1 drive, got %d: %v", len(got), got)
		}
		if got[0].Type != "QLC" {
			t.Errorf("got Type=%q; want %q", got[0].Type, "QLC")
		}
	})

	t.Run("proxy GUID sentinel", func(t *testing.T) {
		const json = `{
      "devices": [
        {
          "path": "/dev/nvmePn1",
          "status": "weka_formatted",
          "physical_uuid": "uuid-sentinel",
          "weka_info": { "cluster_guid": "026938d8-a8a2-4ad4-a316-2f23358a1e7a", "is_proxy": false },
          "hardware": {
            "serial_number": "SERSENTINEL",
            "size_bytes": 17179869184,
            "iu_size": 4096
          }
        }
      ]
    }`
		parsed := mustParseSignDriveList(t, json)
		got := extractProxyDrives(parsed)
		if len(got) != 1 {
			t.Fatalf("expected 1 drive for proxySignedGUID sentinel, got %d: %v", len(got), got)
		}
	})

	t.Run("proxy guid string sentinel", func(t *testing.T) {
		const json = `{
      "devices": [
        {
          "path": "/dev/nvmePGn1",
          "status": "weka_formatted",
          "physical_uuid": "uuid-proxyguid",
          "weka_info": { "cluster_guid": "proxy guid", "is_proxy": false },
          "hardware": {
            "serial_number": "SERPG",
            "size_bytes": 17179869184,
            "iu_size": 4096
          }
        }
      ]
    }`
		parsed := mustParseSignDriveList(t, json)
		got := extractProxyDrives(parsed)
		if len(got) != 1 {
			t.Fatalf("expected 1 drive for 'proxy guid' sentinel, got %d: %v", len(got), got)
		}
	})

	t.Run("status not weka_formatted — skipped", func(t *testing.T) {
		const json = `{
      "devices": [
        {
          "path": "/dev/nvmeXn1",
          "status": "excluded",
          "physical_uuid": "uuid-excl",
          "weka_info": { "cluster_guid": "some-guid", "is_proxy": true },
          "hardware": { "serial_number": "SEREXCL", "size_bytes": 17179869184, "iu_size": 4096 }
        }
      ]
    }`
		parsed := mustParseSignDriveList(t, json)
		got := extractProxyDrives(parsed)
		if len(got) != 0 {
			t.Errorf("expected empty for non-weka_formatted status, got %v", got)
		}
	})

	t.Run("weka_info nil — skipped", func(t *testing.T) {
		const json = `{
      "devices": [
        {
          "path": "/dev/nvmeXn1",
          "status": "weka_formatted",
          "physical_uuid": "uuid-nil",
          "weka_info": null,
          "hardware": { "serial_number": "SERNILWI", "size_bytes": 17179869184, "iu_size": 4096 }
        }
      ]
    }`
		parsed := mustParseSignDriveList(t, json)
		got := extractProxyDrives(parsed)
		if len(got) != 0 {
			t.Errorf("expected empty for nil weka_info, got %v", got)
		}
	})

	t.Run("empty physical_uuid — skipped", func(t *testing.T) {
		const json = `{
      "devices": [
        {
          "path": "/dev/nvmeXn1",
          "status": "weka_formatted",
          "physical_uuid": "",
          "weka_info": { "cluster_guid": "some-guid", "is_proxy": true },
          "hardware": { "serial_number": "SERNOUUID", "size_bytes": 17179869184, "iu_size": 4096 }
        }
      ]
    }`
		parsed := mustParseSignDriveList(t, json)
		got := extractProxyDrives(parsed)
		if len(got) != 0 {
			t.Errorf("expected empty for empty physical_uuid, got %v", got)
		}
	})

	t.Run("zero size_bytes — skipped", func(t *testing.T) {
		const json = `{
      "devices": [
        {
          "path": "/dev/nvmeXn1",
          "status": "weka_formatted",
          "physical_uuid": "uuid-zero",
          "weka_info": { "cluster_guid": "some-guid", "is_proxy": true },
          "hardware": { "serial_number": "SERZERO", "size_bytes": 0, "iu_size": 4096 }
        }
      ]
    }`
		parsed := mustParseSignDriveList(t, json)
		got := extractProxyDrives(parsed)
		if len(got) != 0 {
			t.Errorf("expected empty for zero size_bytes, got %v", got)
		}
	})
}
