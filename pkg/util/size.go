package util

import (
	"fmt"
)

// HumanReadableSize converts a size in bytes to a human-readable string
// with appropriate size units (B, KiB, MiB, GiB, TiB)
func HumanReadableSize(bytes int64) string {
	const (
		KiB = 1024
		MiB = KiB * 1024
		GiB = MiB * 1024
		TiB = GiB * 1024
	)

	absBytes := bytes
	if absBytes < 0 {
		absBytes = -absBytes
	}

	switch {
	case absBytes >= TiB:
		return fmt.Sprintf("%.1fTiB", float64(bytes)/float64(TiB))
	case absBytes >= GiB:
		return fmt.Sprintf("%.1fGiB", float64(bytes)/float64(GiB))
	case absBytes >= MiB:
		return fmt.Sprintf("%.1fMiB", float64(bytes)/float64(MiB))
	case absBytes >= KiB:
		return fmt.Sprintf("%.1fKiB", float64(bytes)/float64(KiB))
	case absBytes == 0:
		return "0B"
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}
