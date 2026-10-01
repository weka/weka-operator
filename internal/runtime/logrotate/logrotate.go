// Package logrotate rewrites and runs logrotate for weka-pod-runtime's syslog-ng-managed
// log files.
package logrotate

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/process"
	"github.com/weka/weka-operator/internal/runtime/syslog"
)

var logrotateConfigPath = "/etc/logrotate.conf" // test seam: overridden in tests to a writable temp path

// logrotateConfig's paths must match the file() destinations in resources/syslog-ng.conf. A path that
// does not exist is skipped silently by missingok, so that file never rotates.
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

// RotateInterval is how often Rotate should be run periodically (e.g. via
// lifecycle.Coordinator.GoPeriodic), with no initial delay.
const RotateInterval = 60 * time.Second

// Applies reports whether periodic logrotate should run for mode: never for adhoc-op,
// and never when syslogPackage resolves to go-syslog, which has no log files for
// logrotate to manage.
func Applies(mode, syslogPackage string) bool {
	return mode != "adhoc-op" && !syslog.UseGoSyslog(syslogPackage)
}

// Rotate rewrites the logrotate config and runs logrotate once.
func Rotate(ctx context.Context, runner process.CommandRunner) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "logrotate.Rotate")
	defer logger.End()

	if err := os.WriteFile(logrotateConfigPath, []byte(logrotateConfig), 0o644); err != nil {
		return fmt.Errorf("write logrotate config: %w", err)
	}
	if _, err := runner.Run(ctx, process.Command{
		Path: "logrotate",
		Args: []string{logrotateConfigPath},
		Log:  process.LogAll,
	}); err != nil {
		return fmt.Errorf("run logrotate: %w", err)
	}
	return nil
}
