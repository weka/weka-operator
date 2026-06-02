// Package cpuaffinity implements CPU core selection and affinity management.
// Mirrors find_full_cores, manage_cpu_affinities, periodic_cpu_affinity_management
// at weka_runtime.py:1647–2073.
package cpuaffinity

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/config"
)

// FindFullCores selects n CPU core IDs suitable for Weka data-path processes.
// Mirrors Python find_full_cores() at weka_runtime.py:1716.
func FindFullCores(ctx context.Context, cfg *config.Config, n int) ([]string, error) {
	_, logger := instrumentation.CreateLogSpan(ctx, "cpuaffinity.FindFullCores")
	defer logger.End()

	// If explicit core IDs are set and not "auto", use them directly.
	if len(cfg.CoreIDs) > 0 {
		result := make([]string, len(cfg.CoreIDs))
		for i, id := range cfg.CoreIDs {
			result[i] = strconv.Itoa(id)
		}
		return result, nil
	}

	available, err := parseCPUAllowedList("/proc/1/status")
	if err != nil {
		return nil, fmt.Errorf("cpuaffinity: reading allowed CPUs: %w", err)
	}

	if cfg.CPUPolicy == "dedicated" {
		selected := make([]string, 0, n)
		for _, c := range available {
			if c != 0 {
				selected = append(selected, strconv.Itoa(c))
				if len(selected) == n {
					return selected, nil
				}
			}
		}
		return nil, fmt.Errorf("cpuaffinity: cannot find %d dedicated cores (found %d)", n, len(selected))
	}

	// Shared (HT) mode: pick one sibling from each fully-available HT pair.
	availSet := make(map[int]struct{}, len(available))
	for _, c := range available {
		availSet[c] = struct{}{}
	}

	var zeroSiblings []int
	if _, ok := availSet[0]; ok {
		if s, err := readSiblingsList(0); err == nil {
			zeroSiblings = s
		}
	}
	zeroSibSet := make(map[int]struct{}, len(zeroSiblings))
	for _, s := range zeroSiblings {
		zeroSibSet[s] = struct{}{}
	}

	var selected []string
	selectedSet := make(map[int]struct{})

	for _, cpu := range available {
		if _, skip := zeroSibSet[cpu]; skip {
			continue
		}
		siblings, err := readSiblingsList(cpu)
		if err != nil {
			continue
		}
		// All siblings must be in the allowed set.
		allAvail := true
		for _, sib := range siblings {
			if _, ok := availSet[sib]; !ok {
				allAvail = false
				break
			}
		}
		if !allAvail {
			continue
		}
		// None of the siblings may already be selected.
		alreadySelected := false
		for _, sib := range siblings {
			if _, ok := selectedSet[sib]; ok {
				alreadySelected = true
				break
			}
		}
		if alreadySelected {
			continue
		}
		selected = append(selected, strconv.Itoa(cpu))
		selectedSet[cpu] = struct{}{}
		if len(selected) == n {
			return selected, nil
		}
	}
	return nil, fmt.Errorf("cpuaffinity: cannot find %d full HT core pairs (found %d)", n, len(selected))
}

// parseCPUAllowedList reads Cpus_allowed_list from /proc/1/status and returns sorted int slice.
// Mirrors Python parse_cpu_allowed_list() at weka_runtime.py:1647.
func parseCPUAllowedList(path string) ([]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() //nolint:errcheck // close error on read-only file is not actionable

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Cpus_allowed_list") {
			parts := strings.SplitN(line, ":\t", 2)
			if len(parts) == 2 {
				return expandRanges(strings.TrimSpace(parts[1])), nil
			}
		}
	}
	return nil, fmt.Errorf("cpus_allowed_list not found in %s", path)
}

// expandRanges expands a range string like "0-3,8-11" into a sorted int slice.
// Mirrors Python expand_ranges() at weka_runtime.py:1655.
func expandRanges(rangesStr string) []int {
	var result []int
	for _, part := range strings.Split(rangesStr, ",") {
		part = strings.TrimSpace(part)
		if idx := strings.Index(part, "-"); idx >= 0 {
			start, err := strconv.Atoi(part[:idx])
			if err != nil {
				continue
			}
			end, err := strconv.Atoi(part[idx+1:])
			if err != nil {
				continue
			}
			for i := start; i <= end; i++ {
				result = append(result, i)
			}
		} else if part != "" {
			if v, err := strconv.Atoi(part); err == nil {
				result = append(result, v)
			}
		}
	}
	return result
}

// readSiblingsList reads the thread_siblings_list for a given CPU index.
// Mirrors Python read_siblings_list() at weka_runtime.py:1666.
func readSiblingsList(cpuIndex int) ([]int, error) {
	path := fmt.Sprintf("/sys/devices/system/cpu/cpu%d/topology/thread_siblings_list", cpuIndex)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return expandRanges(strings.TrimSpace(string(data))), nil
}

// Manager periodically adjusts CPU affinities of non-datapath processes.
type Manager struct {
	cfg *config.Config
}

// NewManager creates a new Manager.
func NewManager(cfg *config.Config) *Manager {
	return &Manager{cfg: cfg}
}

// RunPeriodic runs the affinity management loop in the background.
// Initial delay is 30s, then runs every 60s.
// Mirrors Python periodic_cpu_affinity_management() at weka_runtime.py:2055.
func (m *Manager) RunPeriodic(ctx context.Context) {
	_, logger := instrumentation.CreateLogSpan(ctx, "cpuaffinity.RunPeriodic")
	defer logger.End()

	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}

	logger.Info("starting periodic CPU affinity management (every 60 seconds)")

	for {
		if err := m.Manage(ctx); err != nil {
			logger.Warn("periodic CPU affinity management failed (non-fatal)", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(60 * time.Second):
		}
	}
}

// Manage identifies processes that need reassignment and tasksets them.
// Mirrors Python manage_cpu_affinities() at weka_runtime.py:1963.
func (m *Manager) Manage(ctx context.Context) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "cpuaffinity.Manage")
	defer logger.End()

	available, err := parseCPUAllowedList("/proc/1/status")
	if err != nil {
		return fmt.Errorf("cpuaffinity.Manage: %w", err)
	}

	dataPathCores := getDataPathCores()
	reservedSet := getAllReservedCores(dataPathCores)

	var remaining []int
	for _, c := range available {
		if _, reserved := reservedSet[c]; !reserved {
			remaining = append(remaining, c)
		}
	}

	if len(remaining) == 0 {
		logger.Warn("no remaining cores available for CPU affinity management")
		return nil
	}

	coresStr := intsToCSV(remaining)
	targetSet := make(map[int]struct{}, len(remaining))
	for _, c := range remaining {
		targetSet[c] = struct{}{}
	}

	pids := getProcessesToReassign()
	var needChange []string
	for _, pid := range pids {
		current := getProcessAffinity(pid)
		if current == nil {
			continue
		}
		if mapsEqual(current, targetSet) {
			continue
		}
		needChange = append(needChange, pid)
	}
	if len(needChange) == 0 {
		return nil
	}

	logger.Debug("managing CPU affinities",
		"cores", coresStr,
		"processes_needing_change", len(needChange),
	)

	changesMade := 0
	for _, pid := range needChange {
		cmd := exec.CommandContext(ctx, "taskset", "-cp", coresStr, pid) //nolint:gosec // pid is sourced from ps output, not user input
		if err := cmd.Run(); err != nil {
			logger.Debug("failed to set affinity", "pid", pid, "err", err)
			continue
		}
		changesMade++
	}
	if changesMade > 0 {
		logger.Info("CPU affinity management: adjusted processes", "count", changesMade)
	}
	return nil
}

// getDataPathCores reads wekanode data-path process core affinities.
// Mirrors Python get_data_path_cores() at weka_runtime.py:1767.
func getDataPathCores() []int {
	out, err := exec.Command("ps", "aux").Output() //nolint:gosec // ps with fixed args, no user input
	if err != nil {
		return nil
	}

	var dpPIDs []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "/weka/wekanode") &&
			strings.Contains(line, "--slot") &&
			!strings.Contains(line, "--slot 0") {
			parts := strings.Fields(line)
			if len(parts) > 1 {
				dpPIDs = append(dpPIDs, parts[1])
			}
		}
	}

	coreSet := make(map[int]struct{})
	for _, pid := range dpPIDs {
		affinity := getProcessAffinity(pid)
		for c := range affinity {
			coreSet[c] = struct{}{}
		}
	}
	var result []int
	for c := range coreSet {
		result = append(result, c)
	}
	return result
}

// getAllReservedCores returns data-path cores plus all their HT siblings.
// Mirrors Python get_all_reserved_cores() at weka_runtime.py:1818.
func getAllReservedCores(dataPathCores []int) map[int]struct{} {
	reserved := make(map[int]struct{})
	for _, c := range dataPathCores {
		reserved[c] = struct{}{}
		if siblings, err := readSiblingsList(c); err == nil {
			for _, s := range siblings {
				reserved[s] = struct{}{}
			}
		}
	}
	return reserved
}

// getProcessesToReassign collects PIDs eligible for affinity reassignment.
// Mirrors Python get_processes_to_reassign() at weka_runtime.py:1881.
func getProcessesToReassign() []string {
	out, err := exec.Command("ps", "aux").Output() //nolint:gosec // ps with fixed args, no user input
	if err != nil {
		return nil
	}

	var pids []string
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		pid := parts[1]
		if pid == "PID" || pid == "1" {
			continue
		}
		if strings.Contains(line, "/weka/wekanode") && !strings.Contains(line, "--slot 0") {
			continue
		}
		if uptime := processUptime(pid); uptime < 10.0 {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

// getProcessAffinity returns the current affinity set for a PID, or nil on error.
// Mirrors Python get_process_affinity() at weka_runtime.py:1943.
func getProcessAffinity(pid string) map[int]struct{} {
	out, err := exec.Command("taskset", "-cp", pid).Output() //nolint:gosec // pid is sourced from ps output, not user input
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "affinity list:") {
			parts := strings.SplitN(line, "affinity list:", 2)
			if len(parts) == 2 {
				cores := expandRanges(strings.TrimSpace(parts[1]))
				result := make(map[int]struct{}, len(cores))
				for _, c := range cores {
					result[c] = struct{}{}
				}
				return result
			}
		}
	}
	return nil
}

// processUptime returns how long (seconds) a process has been running, or 0 on error.
func processUptime(pid string) float64 {
	statData, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		return 0
	}
	parts := strings.Fields(string(statData))
	if len(parts) < 22 {
		return 0
	}
	startTicks, err := strconv.ParseFloat(parts[21], 64)
	if err != nil {
		return 0
	}
	uptimeData, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	systemUptime, err := strconv.ParseFloat(strings.Fields(string(uptimeData))[0], 64)
	if err != nil {
		return 0
	}
	clkTck := float64(100) // SC_CLK_TCK default
	return systemUptime - startTicks/clkTck
}

func intsToCSV(ints []int) string {
	parts := make([]string, len(ints))
	for i, v := range ints {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}

func mapsEqual(a, b map[int]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}
