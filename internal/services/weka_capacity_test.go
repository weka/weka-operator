package services

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"testing"
)

// Trimmed `weka debug jrpc filesystems_list` reply; the shape is the same on 4.4, 5.1 and 6.0.
const filesystemsListReply = `{
	"FSId<0>": {"group_id": "FSGroupId<0>", "name": ".config_fs", "is_ready": true, "status": "READY",
		"total_budget": 107374182400, "ssd_budget": 107374182400, "available_ssd": 107374182400,
		"used_total": 12288, "used_ssd": 12288, "obs_buckets": []},
	"FSId<1>": {"group_id": "FSGroupId<0>", "name": "default", "is_ready": true, "status": "READY",
		"total_budget": 34571804672, "ssd_budget": 34571804672, "available_ssd": 34571804672,
		"used_total": 12288, "used_ssd": 12288, "obs_buckets": []}
}`

func TestCapacityFromFilesystemsList(t *testing.T) {
	var byId map[string]WekaFilesystem
	if err := json.Unmarshal([]byte(filesystemsListReply), &byId); err != nil {
		t.Fatal(err)
	}

	got := capacityFromFilesystems(slices.Collect(maps.Values(byId)))
	if len(got.Filesystems) != 2 {
		t.Errorf("got %d filesystems, want 2", len(got.Filesystems))
	}
	got.Filesystems = nil

	const provisioned, used = 107374182400 + 34571804672, 24576
	want := WekaCapacityInfo{
		TotalProvisionedCapacity:    provisioned,
		TotalUsedCapacity:           used,
		TotalAvailableCapacity:      provisioned - used,
		TotalAvailableSSDCapacity:   provisioned,
		TotalProvisionedSSDCapacity: provisioned,
		TotalUsedSSDCapacity:        used,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}
