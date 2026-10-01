// Package syslog adds a syslog daemon to the process supervisor.
// Mirrors start_syslog() at weka_runtime.py:3995.
package syslog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/daemon"
)

const logrotateConfigPath = "/etc/logrotate.conf"

// Paths must match the file() destinations in resources/syslog-ng.conf. A path that
// does not exist is skipped silently by missingok, so that file never rotates.
// Mirrors Python write_logrotate_config() at weka_runtime.py:2413.
const logrotateConfig = `/var/log/syslog /var/log/error {
    size 1M
    rotate 10
    missingok
    notifempty
    compress
    delaycompress
    postrotate
      if [ -f /var/run/syslog-ng.pid ]; then
        kill -HUP $(cat /var/run/syslog-ng.pid)
      else
        echo "syslog-ng.pid not found, skipping reload" >&2
      fi
    endscript
}
`

// AddToDaemon registers the appropriate syslog daemon with the supervisor and,
// for syslog-ng, starts the periodic logrotate loop.
// Mirrors Python start_syslog() at weka_runtime.py:4462 and the periodic_logrotate
// task creation gated on `MODE not in ["adhoc-op"] and not use_go_syslog()`.
func AddToDaemon(ctx context.Context, sup *daemon.Supervisor, cfg *config.Config) {
	cmd, args := chooseSyslog(cfg.SyslogPackage)
	if !useGoSyslog(cfg.SyslogPackage) {
		stripMongodbModule(ctx)
		if cfg.Mode != "adhoc-op" {
			go periodicLogrotate(ctx)
		}
	}
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

// stripMongodbModule removes syslog-ng's mongodb output module. syslog-ng auto-loads
// every module in its module path, and libafmongodb.so links libmongoc, whose
// constructor orphans a deleted-but-open /dev/shm counters inode on every reload.
// No destination here uses mongodb(), so the module is only a liability.
// Mirrors Python strip_syslog_ng_mongodb_module() at weka_runtime.py:300.
func stripMongodbModule(ctx context.Context) {
	_, logger := instrumentation.CreateLogSpan(ctx, "syslog.stripMongodbModule")
	defer logger.End()

	found, err := filepath.Glob("/usr/lib*/syslog-ng/*/libafmongodb.so")
	if err != nil {
		logger.Warn("glob for libafmongodb.so failed", "err", err)
		return
	}
	if len(found) == 0 {
		logger.Warn("libafmongodb.so not found in syslog-ng module path, /dev/shm may accumulate mongoc segments")
		return
	}
	for _, so := range found {
		if err := os.Remove(so); err != nil {
			logger.Warn("could not remove syslog-ng mongodb module", "path", so, "err", err)
			continue
		}
		logger.Info("removed unused syslog-ng module", "path", so)
	}
}

// periodicLogrotate rewrites the logrotate config and runs logrotate every 60s
// until ctx is cancelled. Mirrors Python periodic_logrotate() at weka_runtime.py:2436.
func periodicLogrotate(ctx context.Context) {
	_, logger := instrumentation.CreateLogSpan(ctx, "syslog.periodicLogrotate")
	defer logger.End()

	for {
		if err := os.WriteFile(logrotateConfigPath, []byte(logrotateConfig), 0o644); err != nil {
			logger.Warn("failed to write logrotate config", "err", err)
		} else if err := cmdutil.Run(ctx, "logrotate", logrotateConfigPath); err != nil {
			logger.Warn("logrotate failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(60 * time.Second):
		}
	}
}
