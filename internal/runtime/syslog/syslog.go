// Package syslog builds the syslog daemon command for the process supervisor.
// Mirrors start_syslog() at weka_runtime.py:3995.
package syslog

import (
	"context"
	"os"
	"path/filepath"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// Command returns the syslog daemon command to run, choosing go-syslog or syslog-ng
// per pkg. It does not start the process.
// Mirrors Python start_syslog() at weka_runtime.py:4462.
func Command(pkg string) (process.Command, error) {
	if UseGoSyslog(pkg) {
		return process.Command{Path: "/usr/sbin/go-syslog"}, nil
	}
	return process.Command{
		Path: "/usr/sbin/syslog-ng",
		Args: []string{"-F", "-f", "/etc/syslog-ng/syslog-ng.conf", "--pidfile", "/var/run/syslog-ng.pid"},
	}, nil
}

// UseGoSyslog reports whether pkg resolves to go-syslog: explicitly, or ("auto"/empty)
// by probing for the go-syslog binary.
func UseGoSyslog(pkg string) bool {
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

// StripMongodbModule removes syslog-ng's mongodb output module. syslog-ng auto-loads
// every module in its module path, and libafmongodb.so links libmongoc, whose
// constructor orphans a deleted-but-open /dev/shm counters inode on every reload.
// No destination here uses mongodb(), so the module is only a liability.
// Mirrors Python strip_syslog_ng_mongodb_module() at weka_runtime.py:300.
func StripMongodbModule(ctx context.Context) {
	_, logger := instrumentation.CreateLogSpan(ctx, "syslog.StripMongodbModule")
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
