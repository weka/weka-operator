// Package syslog adds a syslog daemon to the process supervisor.
// Mirrors start_syslog() at weka_runtime.py:3995.
package syslog

import (
	"os"
	"os/exec"

	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/daemon"
)

// AddToDaemon registers the appropriate syslog daemon with the supervisor.
// Mirrors Python start_syslog() at weka_runtime.py:3995.
//
// Syslog selection rules (matching Python use_go_syslog()):
//   - SyslogPackage == "auto":  use go-syslog if /usr/sbin/go-syslog exists, else syslog-ng
//   - SyslogPackage == "go-syslog": always use go-syslog
//   - SyslogPackage == "syslog-ng": always use syslog-ng
func AddToDaemon(sup *daemon.Supervisor, cfg *config.Config) {
	cmd, args := chooseSyslog(cfg.SyslogPackage)
	sup.Add("syslog", func() *exec.Cmd {
		return exec.Command(cmd, args...) //nolint:gosec // path is a known binary
	})
}

func chooseSyslog(pkg string) (cmd string, args []string) {
	if useGoSyslog(pkg) {
		return "/usr/sbin/go-syslog", nil
	}
	return "/usr/sbin/syslog-ng", []string{"-F", "-f", "/etc/syslog-ng/syslog-ng.conf", "--pidfile", "/var/run/syslog-ng.pid"}
}

func useGoSyslog(pkg string) bool {
	switch pkg {
	case "go-syslog":
		return true
	case "syslog-ng":
		return false
	default: // "auto" or empty
		_, err := os.Stat("/usr/sbin/go-syslog")
		return err == nil
	}
}
