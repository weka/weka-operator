package util

import (
	"fmt"
	"math/big"
	"net"
	"sort"
	"strings"

	"github.com/pkg/errors"
)

func IsIpv6(ip string) bool {
	parsedIP := net.ParseIP(ip)
	return parsedIP != nil && parsedIP.To4() == nil
}

// ipToInt converts net.IP to big.Int for arithmetic operations
func ipToInt(ip net.IP) *big.Int {
	return new(big.Int).SetBytes(ip)
}

// intToIP converts big.Int back to net.IP, preserving IPv4/IPv6 format
func intToIP(i *big.Int, isIPv4 bool) net.IP {
	ipBytes := i.Bytes()

	if isIPv4 {
		// IPv4: 4 bytes
		if len(ipBytes) < 4 {
			padded := make([]byte, 4)
			copy(padded[4-len(ipBytes):], ipBytes)
			return net.IP(padded)
		}
		if len(ipBytes) > 4 {
			return net.IP(ipBytes[len(ipBytes)-4:])
		}
		return net.IP(ipBytes)
	}

	// IPv6: 16 bytes
	if len(ipBytes) < 16 {
		padded := make([]byte, 16)
		copy(padded[16-len(ipBytes):], ipBytes)
		return net.IP(padded)
	}
	return net.IP(ipBytes)
}

// expandSingleIPRange expands a single IP range from start to end
func expandSingleIPRange(start, end net.IP) ([]string, error) {
	isIPv4 := start.To4() != nil
	if isIPv4 {
		start = start.To4()
		end = end.To4()
	} else {
		start = start.To16()
		end = end.To16()
	}

	startInt := ipToInt(start)
	endInt := ipToInt(end)

	if startInt.Cmp(endInt) > 0 {
		return nil, errors.Errorf("start IP %s is greater than end IP %s", start, end)
	}

	var ips []string
	for i := new(big.Int).Set(startInt); i.Cmp(endInt) <= 0; i.Add(i, big.NewInt(1)) {
		ip := intToIP(new(big.Int).Set(i), isIPv4)
		ips = append(ips, ip.String())
	}

	return ips, nil
}

// ExpandIpRanges expands IP range strings into individual IP addresses.
// Supports multiple formats:
//   - Single IP: "10.200.1.200"
//   - Full range: "10.200.1.200-10.200.1.203"
//   - Short form (IPv4 only): "10.200.1.200-203"
//   - IPv6 ranges: "2001:db8::1-2001:db8::4"
//
// Uses net.ParseIP for robust validation and supports both IPv4 and IPv6.
func ExpandIpRanges(ipRanges []string) ([]string, error) {
	var result []string
	for _, ipRange := range ipRanges {
		ipRange = strings.TrimSpace(ipRange)

		// Check if it's a range (contains '-')
		if strings.Contains(ipRange, "-") {
			parts := strings.SplitN(ipRange, "-", 2)
			if len(parts) != 2 {
				return nil, errors.Errorf("invalid IP range format: %s", ipRange)
			}

			startStr := strings.TrimSpace(parts[0])
			endStr := strings.TrimSpace(parts[1])

			// Parse start IP
			startIP := net.ParseIP(startStr)
			if startIP == nil {
				return nil, errors.Errorf("invalid start IP: %s", startStr)
			}

			// Parse end IP - might be a full IP or just the last part
			endIP := net.ParseIP(endStr)
			if endIP == nil {
				// Try short form for IPv4 (e.g., "10.200.1.200-203")
				if startIP.To4() != nil && !strings.Contains(endStr, ":") {
					// Extract prefix from start IP
					startParts := strings.Split(startStr, ".")
					if len(startParts) == 4 {
						// Assume endStr is just the last octet
						endIPStr := fmt.Sprintf("%s.%s.%s.%s", startParts[0], startParts[1], startParts[2], endStr)
						endIP = net.ParseIP(endIPStr)
					}
				}

				if endIP == nil {
					return nil, errors.Errorf("invalid end IP: %s", endStr)
				}
			}

			// Expand the range
			ips, err := expandSingleIPRange(startIP, endIP)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to expand range %s", ipRange)
			}
			result = append(result, ips...)
		} else {
			// Single IP - validate it
			ip := net.ParseIP(ipRange)
			if ip == nil {
				return nil, errors.Errorf("invalid IP address: %s", ipRange)
			}
			result = append(result, ip.String())
		}
	}
	return result, nil
}

// AggregateIpsToRanges converts a list of individual IPs into ranges to reduce API calls.
// Examples:
//   - IPv4: ["10.200.1.200", "10.200.1.201", "10.200.1.202"] -> ["10.200.1.200-202"]
//   - IPv6: ["2001:db8::1", "2001:db8::2", "2001:db8::3"] -> ["2001:db8::1-2001:db8::3"]
//
// Uses net.ParseIP for validation and supports both IPv4 and IPv6.
func AggregateIpsToRanges(ips []string) ([]string, error) {
	if len(ips) == 0 {
		return []string{}, nil
	}

	// Parse and validate all IPs, convert to big.Int for sorting
	type ipInfo struct {
		original string
		parsed   net.IP
		intVal   *big.Int
		isIPv4   bool
	}

	var ipInfos []ipInfo
	for _, ipStr := range ips {
		parsed := net.ParseIP(ipStr)
		if parsed == nil {
			return nil, errors.Errorf("invalid IP address: %s", ipStr)
		}

		isIPv4 := parsed.To4() != nil
		if isIPv4 {
			parsed = parsed.To4()
		} else {
			parsed = parsed.To16()
		}

		ipInfos = append(ipInfos, ipInfo{
			original: parsed.String(), // Normalize the IP string
			parsed:   parsed,
			intVal:   ipToInt(parsed),
			isIPv4:   isIPv4,
		})
	}

	// Sort by integer value
	sort.Slice(ipInfos, func(i, j int) bool {
		return ipInfos[i].intVal.Cmp(ipInfos[j].intVal) < 0
	})

	// Build ranges
	var ranges []string
	i := 0
	for i < len(ipInfos) {
		rangeStart := ipInfos[i]
		rangeEnd := rangeStart

		// Find consecutive IPs (same IP version)
		one := big.NewInt(1)
		for j := i + 1; j < len(ipInfos); j++ {
			if ipInfos[j].isIPv4 != rangeStart.isIPv4 {
				break // Different IP version
			}

			// Check if next IP is consecutive
			expected := new(big.Int).Add(rangeEnd.intVal, one)
			if ipInfos[j].intVal.Cmp(expected) != 0 {
				break // Not consecutive
			}

			rangeEnd = ipInfos[j]
		}

		// Create range string
		if rangeStart.intVal.Cmp(rangeEnd.intVal) == 0 {
			// Single IP
			ranges = append(ranges, rangeStart.original)
		} else {
			// Range - use short form for IPv4
			if rangeStart.isIPv4 {
				// IPv4: use short form (e.g., "10.200.1.200-202")
				startParts := strings.Split(rangeStart.original, ".")
				endParts := strings.Split(rangeEnd.original, ".")
				if len(startParts) == 4 && len(endParts) == 4 {
					ranges = append(ranges, fmt.Sprintf("%s-%s", rangeStart.original, endParts[3]))
				} else {
					ranges = append(ranges, fmt.Sprintf("%s-%s", rangeStart.original, rangeEnd.original))
				}
			} else {
				// IPv6: use full form
				ranges = append(ranges, fmt.Sprintf("%s-%s", rangeStart.original, rangeEnd.original))
			}
		}

		// Calculate number of IPs in this range
		count := new(big.Int).Sub(rangeEnd.intVal, rangeStart.intVal)
		count.Add(count, big.NewInt(1))
		i += int(count.Int64())
	}

	return ranges, nil
}
