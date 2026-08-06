package services

// HealthyClusterStatuses are the weka cluster statuses that gate a disruptive operation
// (upgrade, ssdproxy rotation): OK or actively REDISTRIBUTING.
var HealthyClusterStatuses = []string{"OK", "REDISTRIBUTING"}

// MeetsThreshold reports whether active is at least thresholdPercent of total. A total of 0
// always passes: there is nothing of that kind to threshold against.
func MeetsThreshold(active, total, thresholdPercent int) bool {
	if total == 0 {
		return true
	}
	threshold := float64(total) * float64(thresholdPercent) / 100
	return float64(active) >= threshold
}
