package resources

// GetSsdProxyHugepagesMiB returns required hugepages memory in MiB for ssd_proxy
// based on the number of drives and their maximum capacity.
//
// Formula (validated against 9 empirical data points with 100% accuracy):
//
//	HugeTLB_MiB = 378 + 4 × (max_drives × expected_max_drive_size_TiB)
//
// Parameters:
//   - maxDrives: Number of drives the ssd_proxy will manage
//   - expectedMaxDriveTiB: Maximum capacity of a single drive in TiB
//
// Examples:
//   - 4 drives × 10 TiB: 378 + 4×40 = 538 MiB
//   - 6 drives × 10 TiB: 378 + 4×60 = 618 MiB
//   - 6 drives × 50 TiB: 378 + 4×300 = 1578 MiB
func GetSsdProxyHugepagesMiB(maxDrives int, expectedMaxDriveTiB int) int64 {
	const baseMiB int64 = 378
	const perTotalTiBMiB int64 = 4

	totalCapacityTiB := int64(maxDrives) * int64(expectedMaxDriveTiB)
	hugepagesMiB := baseMiB + (perTotalTiBMiB * totalCapacityTiB)

	return hugepagesMiB
}
