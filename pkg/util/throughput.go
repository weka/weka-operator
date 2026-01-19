package util

import (
	"fmt"
)

func HumanReadableThroughput(bytesPerSecond float64) string {
	const (
		KiB = 1024
		MiB = KiB * 1024
		GiB = MiB * 1024
		TiB = GiB * 1024
	)

	switch {
	case bytesPerSecond >= TiB:
		return fmt.Sprintf("%.1fTiB/s", bytesPerSecond/TiB)
	case bytesPerSecond >= GiB:
		return fmt.Sprintf("%.1fGiB/s", bytesPerSecond/GiB)
	case bytesPerSecond >= MiB:
		return fmt.Sprintf("%.1fMiB/s", bytesPerSecond/MiB)
	case bytesPerSecond >= KiB:
		return fmt.Sprintf("%.1fKiB/s", bytesPerSecond/KiB)
	case bytesPerSecond < 1:
		return "--"
	default:
		return fmt.Sprintf("%.0fB/s", bytesPerSecond)
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
		return fmt.Sprintf("%.1fT", iops/T)
	case iops >= G:
		return fmt.Sprintf("%.1fG", iops/G)
	case iops >= M:
		return fmt.Sprintf("%.1fM", iops/M)
	case iops >= K:
		return fmt.Sprintf("%.1fK", iops/K)
	case iops < 1:
		return "--"
	default:
		return fmt.Sprintf("%.0f", iops)
	}
}
