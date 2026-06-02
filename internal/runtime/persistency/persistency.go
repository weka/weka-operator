// Package persistency sets up persistent storage bind-mounts for the Weka pod runtime.
// It mirrors configure_persistency() at weka_runtime.py:2835.
package persistency

import (
	"fmt"
	"os"

	"context"

	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	"github.com/weka/weka-operator/internal/runtime/config"
)

const (
	persistencyConfiguredPath = "/opt/weka/k8s-runtime/persistency-configured"
	wekaK8sRuntimeDir         = "/opt/weka/k8s-runtime"
)

// Configure sets up persistent storage bind-mounts.
// Mirrors Python configure_persistency() at weka_runtime.py:2835.
func Configure(ctx context.Context, cfg *config.Config) error {
	persistenceDir := "/host-binds/opt-weka"
	if cfg.WekaPersistenceMode == "global" {
		persistenceDir = fmt.Sprintf("/opt/weka-global-persistence/containers/%s", cfg.WekaContainerID)
	}

	script := fmt.Sprintf(`
if [ -d /host-binds/opt-weka ]; then
    mkdir -p /opt/weka-preinstalled
    mount -o bind /opt/weka /opt/weka-preinstalled
    mkdir -p %s/dist/drivers
    mount -o bind %s/dist/drivers /opt/weka-preinstalled/dist/drivers
    mount -o bind %s /opt/weka
    mkdir -p /opt/weka/dist
    mount -o bind /opt/weka-preinstalled/dist /opt/weka/dist
    mount -o bind %s/dist/drivers /opt/weka/dist/drivers
fi

if [ -d /host-binds/boot-level ]; then
    BOOT_DIR=/host-binds/boot-level/$(cat /proc/sys/kernel/random/boot_id)/cleanup
    mkdir -p $BOOT_DIR
    mkdir -p /opt/weka/external-mounts/cleanup
    mount -o bind $BOOT_DIR /opt/weka/external-mounts/cleanup
fi

if [ -d /host-binds/ssdproxy ]; then
    mkdir -p /opt/weka/external-mounts/ssdproxy
    mount -o bind /host-binds/ssdproxy /opt/weka/external-mounts/ssdproxy
fi

if [ -d /host-binds/shared ]; then
    mkdir -p /host-binds/shared/local-sockets
    mkdir -p /opt/weka/external-mounts/local-sockets
    mount -o bind /host-binds/shared/local-sockets /opt/weka/external-mounts/local-sockets
fi

if [ -f /var/run/secrets/weka-operator/wekahome-cacert/cert.pem ]; then
    rm -rf /opt/weka/k8s-runtime/vars/wh-cacert
    mkdir -p /opt/weka/k8s-runtime/vars/wh-cacert/
    cp /var/run/secrets/weka-operator/wekahome-cacert/cert.pem /opt/weka/k8s-runtime/vars/wh-cacert/cert.pem
    chmod 400 /opt/weka/k8s-runtime/vars/wh-cacert/cert.pem
fi

if [ -d /host-binds/shared-configs ]; then
    mkdir -p /opt/weka/external-mounts/shared_boot_level
    mount -o bind /host-binds/shared-configs /opt/weka/external-mounts/shared_boot_level
    ENVOY_DIR=/opt/weka/envoy
    EXT_ENVOY_DIR=/host-binds/shared-configs/envoy
    mkdir -p $ENVOY_DIR
    mkdir -p $EXT_ENVOY_DIR
    mount -o bind $EXT_ENVOY_DIR $ENVOY_DIR
    mkdir -p /opt/weka/wtracer
    mkdir -p /host-binds/shared-configs/audit-traces
    mount -o bind /host-binds/shared-configs/audit-traces /opt/weka/wtracer
fi

mkdir -p %s
touch %s
`,
		persistenceDir, persistenceDir, persistenceDir, persistenceDir,
		wekaK8sRuntimeDir, persistencyConfiguredPath,
	)

	if err := cmdutil.Run(ctx, "sh", "-c", script); err != nil {
		return fmt.Errorf("configure_persistency: %w", err)
	}
	return nil
}

// IsConfigured reports whether persistency has been configured.
func IsConfigured() bool {
	_, err := os.Stat(persistencyConfiguredPath)
	return err == nil
}
