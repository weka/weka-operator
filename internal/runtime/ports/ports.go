// Package ports allocates weka container port ranges for client mode.
// Mirrors ensure_client_ports, get_free_subrange_in_port_range at weka_runtime.py:3527–3552.
package ports

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/config"
)

const (
	subrangeSize = 100 // WEKA_CONTAINER_PORT_SUBRANGE
	maxPort      = 65535
)

// SavePorts writes the current ports in cfg to the runtime vars files.
// Called after port allocation (client) and after loading resources.json (compute/drive/etc).
// Mirrors Python save_weka_ports_data() at weka_runtime.py:3554.
func SavePorts(_ context.Context, cfg *config.Config) error {
	return savePorts(cfg)
}

// AllocateClientPorts finds free ports and writes them to runtime files.
// Mirrors Python ensure_client_ports() at weka_runtime.py:3527.
// No-op when cfg.Port != 0 (ports already assigned via resources).
func AllocateClientPorts(ctx context.Context, cfg *config.Config) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "ports.AllocateClientPorts")
	defer logger.End()

	if cfg.Port != 0 && cfg.AgentPort != 0 {
		// Already have ports from environment; just persist them.
		return savePorts(cfg)
	}

	// Mirror Python assert base_port > 0, "BASE_PORT is not set" at weka_runtime.py:3537.
	base := cfg.BasePort
	if base == 0 {
		return fmt.Errorf("ports: BASE_PORT is not set")
	}
	portRange := cfg.PortRange
	// Mirror Python: max_port = base_port + port_range if port_range > 0 else MAX_PORT (weka_runtime.py:3538).
	// MAX_PORT = 65535.
	top := maxPort
	if portRange > 0 {
		top = base + portRange
		if top > maxPort {
			top = maxPort
		}
	}

	inUse, err := readInUsePorts()
	if err != nil {
		return fmt.Errorf("ports: reading in-use ports: %w", err)
	}

	if cfg.AgentPort == 0 {
		agentPort, err := findFreePort(base, top, inUse, nil)
		if err != nil {
			return fmt.Errorf("ports: find agent port: %w", err)
		}
		cfg.AgentPort = agentPort
	}

	if cfg.Port == 0 {
		p, err := getFreeSubrange(base, top, subrangeSize, inUse, []int{cfg.AgentPort})
		if err != nil {
			return fmt.Errorf("ports: find port subrange: %w", err)
		}
		cfg.Port = p
	}

	return savePorts(cfg)
}

// savePorts writes the port vars files.
// Mirrors Python save_weka_ports_data() at weka_runtime.py:3554-3557 which writes ONLY
// vars/port and vars/agent_port — no weka-ports-data.json (that file is never written by Python).
func savePorts(cfg *config.Config) error {
	if err := os.MkdirAll("/opt/weka/k8s-runtime/vars", 0o755); err != nil {
		return err
	}
	if err := os.WriteFile("/opt/weka/k8s-runtime/vars/port", []byte(strconv.Itoa(cfg.Port)), 0o644); err != nil {
		return err
	}
	return os.WriteFile("/opt/weka/k8s-runtime/vars/agent_port", []byte(strconv.Itoa(cfg.AgentPort)), 0o644)
}

// readInUsePorts parses /proc/net/tcp, tcp6, udp, udp6 and returns the set of in-use ports.
func readInUsePorts() (map[int]struct{}, error) {
	inUse := make(map[int]struct{})
	files := []string{"/proc/net/tcp", "/proc/net/tcp6", "/proc/net/udp", "/proc/net/udp6"}
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		scanner := bufio.NewScanner(f)
		scanner.Scan() // skip header
		for scanner.Scan() {
			cols := strings.Fields(scanner.Text())
			if len(cols) < 2 {
				continue
			}
			// Column 1: "IP:PORT" (hex)
			addrParts := strings.SplitN(cols[1], ":", 2)
			if len(addrParts) != 2 {
				continue
			}
			port, err := strconv.ParseInt(addrParts[1], 16, 32)
			if err != nil || port == 0 {
				continue
			}
			inUse[int(port)] = struct{}{}
		}
		_ = f.Close() //nolint:errcheck // read-only file, close error is not actionable
	}
	return inUse, nil
}

// findFreePort finds a single free port in [base, top) not in inUse or exclude.
func findFreePort(base, top int, inUse map[int]struct{}, exclude []int) (int, error) {
	excludeSet := make(map[int]struct{}, len(exclude))
	for _, p := range exclude {
		excludeSet[p] = struct{}{}
	}
	for p := base; p < top; p++ {
		if _, used := inUse[p]; used {
			continue
		}
		if _, excl := excludeSet[p]; excl {
			continue
		}
		return p, nil
	}
	return 0, fmt.Errorf("no free port in [%d, %d)", base, top)
}

// getFreeSubrange finds the first window of `size` consecutive free ports in [base, top).
// Mirrors Python get_free_subrange_in_port_range() at weka_runtime.py:3470.
func getFreeSubrange(base, top, size int, inUse map[int]struct{}, exclude []int) (int, error) {
	excludeSet := make(map[int]struct{}, len(exclude))
	for _, p := range exclude {
		excludeSet[p] = struct{}{}
	}

	for start := base; start <= top-size; start++ {
		// Skip if start itself is excluded.
		if _, ex := excludeSet[start]; ex {
			continue
		}
		allFree := true
		for p := start; p < start+size; p++ {
			if _, used := inUse[p]; used {
				allFree = false
				start = p // jump past the blocked port
				break
			}
			if _, ex := excludeSet[p]; ex {
				allFree = false
				start = p
				break
			}
		}
		if allFree {
			return start, nil
		}
	}
	return 0, fmt.Errorf("no free %d-port subrange in [%d, %d)", size, base, top)
}
