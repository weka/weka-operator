package weka

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var sizePattern = regexp.MustCompile(`^(\d+)([KMGTPE]I?B)$`)

var sizeMultipliers = map[string]int64{
	"B":   1,
	"KB":  1e3,
	"MB":  1e6,
	"GB":  1e9,
	"TB":  1e12,
	"PB":  1e15,
	"EB":  1e18,
	"KIB": 1 << 10,
	"MIB": 1 << 20,
	"GIB": 1 << 30,
	"TIB": 1 << 40,
	"PIB": 1 << 50,
	"EIB": 1 << 60,
}

// ParseSize parses a size string such as "512GiB" or "10MB" into bytes.
// Mirrors Python convert_to_bytes() at weka_runtime.py:2795.
func ParseSize(s string) (int64, error) {
	upper := strings.ToUpper(s)
	m := sizePattern.FindStringSubmatch(upper)
	if m == nil {
		return 0, fmt.Errorf("invalid size format: %s", upper)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size format: %s", upper)
	}
	return n * sizeMultipliers[m[2]], nil
}
