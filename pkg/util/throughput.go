package util

import (
	"fmt"
)

func HumanReadableThroughput(bytesPerSecond float64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
		TB = GB * 1024
	)

	switch {
	case bytesPerSecond >= TB:
		return fmt.Sprintf("%.1fTB", bytesPerSecond/TB)
	case bytesPerSecond >= GB:
		return fmt.Sprintf("%.1fGB", bytesPerSecond/GB)
	case bytesPerSecond >= MB:
		return fmt.Sprintf("%.1fMB", bytesPerSecond/MB)
	case bytesPerSecond >= KB:
		return fmt.Sprintf("%.1fKB", bytesPerSecond/KB)
	case bytesPerSecond < 1:
		return "--"
	default:
		return fmt.Sprintf("%.0fB", bytesPerSecond)
	}
}

func HumanReadableIops(iops float64) string {
	const (
		K = 1000
		M = K * 1000
		G = M * 1000
		T = G * 1000
	)
	switch {
	case iops >= T:
		return fmt.Sprintf("%.1fT", iops)
	case iops >= G:
		return fmt.Sprintf("%.1fG", iops)
	case iops >= M:
		return fmt.Sprintf("%.1fM", iops)
	case iops >= K:
		return fmt.Sprintf("%.1fK", iops)
	case iops < 1:
		return "--"
	default:
		return fmt.Sprintf("%.0f", iops)
	}
}
