package util

import (
	"fmt"
)

// humanReadableSizeParts converts a size in bytes into its numeric part ("28.0")
// and unit suffix ("TiB") separately, so callers can either join them or lay
// them out independently.
func humanReadableSizeParts(bytes int64) (num, unit string) {
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
		return fmt.Sprintf("%.1f", float64(bytes)/float64(TiB)), "TiB"
	case absBytes >= GiB:
		return fmt.Sprintf("%.1f", float64(bytes)/float64(GiB)), "GiB"
	case absBytes >= MiB:
		return fmt.Sprintf("%.1f", float64(bytes)/float64(MiB)), "MiB"
	case absBytes >= KiB:
		return fmt.Sprintf("%.1f", float64(bytes)/float64(KiB)), "KiB"
	case absBytes == 0:
		return "0", "B"
	default:
		return fmt.Sprintf("%d", bytes), "B"
	}
}

// HumanReadableSize converts a size in bytes to a human-readable string
// with appropriate size units (B, KiB, MiB, GiB, TiB)
func HumanReadableSize(bytes int64) string {
	num, unit := humanReadableSizeParts(bytes)
	return num + unit
}

// humanReadableGiBParts is humanReadableSizeParts for a whole-GiB capacity.
func humanReadableGiBParts(gib int) (num, unit string) {
	return humanReadableSizeParts(int64(gib) * 1024 * 1024 * 1024)
}

// HumanReadableGiB formats a whole-GiB capacity (the unit used throughout the capacity
// planner and container specs) as a human-readable binary size (GiB/TiB/...).
func HumanReadableGiB(gib int) string {
	num, unit := humanReadableGiBParts(gib)
	return num + unit
}

// FormatTlcQlcColumn renders TLC/QLC capacities (whole GiB) as a compact
// Capacity printer-column string. When both values resolve to the same unit
// the unit is factored out ("T/Q 28.0/28.0 TiB"); otherwise per-value units
// are kept ("T/Q 500.0GiB/2.0TiB"). Single-type cases use a short prefix.
func FormatTlcQlcColumn(tlcGiB, qlcGiB int) string {
	switch {
	case tlcGiB > 0 && qlcGiB > 0:
		tn, tu := humanReadableGiBParts(tlcGiB)
		qn, qu := humanReadableGiBParts(qlcGiB)
		if tu == qu {
			return fmt.Sprintf("T/Q %s/%s %s", tn, qn, tu)
		}
		return fmt.Sprintf("T/Q %s%s/%s%s", tn, tu, qn, qu)
	case qlcGiB > 0:
		return fmt.Sprintf("Q %s", HumanReadableGiB(qlcGiB))
	case tlcGiB > 0:
		return fmt.Sprintf("T %s", HumanReadableGiB(tlcGiB))
	default:
		return ""
	}
}
