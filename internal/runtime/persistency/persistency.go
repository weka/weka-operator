// Package persistency sets up persistent storage bind-mounts for the Weka pod runtime.
// It mirrors configure_persistency() at weka_runtime.py:3144.
package persistency

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	"github.com/weka/weka-operator/internal/runtime/config"
)

const (
	persistencyConfiguredPath = "/opt/weka/k8s-runtime/persistency-configured"
	wekaK8sRuntimeDir         = "/opt/weka/k8s-runtime"

	wekahomeCACertSecretDir = "/var/run/secrets/weka-operator/wekahome-cacert"
	wekahomeCACertOutDir    = wekaK8sRuntimeDir + "/vars/wh-cacert"
	wekahomeCACertOutFile   = wekahomeCACertOutDir + "/cert.pem"
)

// Configure sets up persistent storage bind-mounts.
// Mirrors Python configure_persistency() at weka_runtime.py:3144.
func Configure(ctx context.Context, cfg *config.Config) error {
	persistenceDir := "/host-binds/opt-weka"
	if cfg.WekaPersistenceMode == "global" {
		persistenceDir = fmt.Sprintf("/opt/weka-global-persistence/containers/%s", cfg.WekaContainerID)
	}

	mountScript := buildMountScript(persistenceDir)

	if err := cmdutil.Run(ctx, "sh", "-c", mountScript); err != nil {
		return fmt.Errorf("configure_persistency: %w", err)
	}

	if err := configureWHCACert(); err != nil {
		return fmt.Errorf("configure_persistency: wh-cacert: %w", err)
	}

	finalScript := fmt.Sprintf(`
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
		wekaK8sRuntimeDir, persistencyConfiguredPath,
	)

	if err := cmdutil.Run(ctx, "sh", "-c", finalScript); err != nil {
		return fmt.Errorf("configure_persistency: %w", err)
	}
	return nil
}

// buildMountScript renders the bind-mount shell script for the given persistence
// directory. Extracted so tests can inspect the exact script Configure runs
// instead of hand-copying it (which can drift from production).
func buildMountScript(persistenceDir string) string {
	return fmt.Sprintf(`
if [ -d /host-binds/opt-weka ]; then
    mkdir -p /opt/weka-dist-save
    mount -o bind /opt/weka/dist /opt/weka-dist-save
    mount --make-private /opt/weka-dist-save
    BIN_EXISTED=0
    if [ -d /opt/weka/bin ]; then
        BIN_EXISTED=1
        mkdir -p /opt/weka-bin-save
        mount -o bind /opt/weka/bin /opt/weka-bin-save
        mount --make-private /opt/weka-bin-save
    fi
    mkdir -p %s/dist/drivers
    cp -an /opt/weka/dist/drivers/. %s/dist/drivers/ \
        || echo "warning: failed to stage image drivers into %s/dist/drivers" >&2
    mkdir -p /opt/weka-drivers-save
    mount -o bind %s/dist/drivers /opt/weka-drivers-save
    mount --make-private /opt/weka-drivers-save
    mount -o bind %s /opt/weka
    mkdir -p /opt/weka/dist
    mount -o bind /opt/weka-dist-save /opt/weka/dist
    umount /opt/weka-dist-save
    if [ "$BIN_EXISTED" = "1" ]; then
        mkdir -p /opt/weka/bin
        mount -o bind /opt/weka-bin-save /opt/weka/bin
        umount /opt/weka-bin-save
    fi
    mkdir -p /opt/weka/dist/drivers
    mount -o bind /opt/weka-drivers-save /opt/weka/dist/drivers
    umount /opt/weka-drivers-save
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

if [ -d /host-binds/shared-netns ]; then
    mkdir -p /opt/weka/external-mounts/shared-netns
    mount --rbind /host-binds/shared-netns /opt/weka/external-mounts/shared-netns
    mount --make-rshared /opt/weka/external-mounts/shared-netns
fi
`,
		persistenceDir, persistenceDir, persistenceDir, persistenceDir, persistenceDir,
	)
}

// IsConfigured reports whether persistency has been configured.
func IsConfigured() bool {
	_, err := os.Stat(persistencyConfiguredPath)
	return err == nil
}

// caCertFile is one candidate source file for the WekaHome CA bundle.
type caCertFile struct {
	name string
	data []byte
}

// filterCACertContent concatenates raw file contents into the WekaHome CA bundle, keeping
// only entries that contain a certificate and no private key material. This is the same
// per-file test as Python's `grep -q "BEGIN CERTIFICATE" "$f"` / `grep -q "PRIVATE KEY" "$f"`
// (weka_runtime.py:3249-3253): a file with both a cert and a key is skipped whole, not just
// its key portion, so a kubernetes.io/tls secret's tls.key never ends up in the bundle.
func filterCACertContent(files []caCertFile) []byte {
	var buf bytes.Buffer
	for _, f := range files {
		if !bytes.Contains(f.data, []byte("BEGIN CERTIFICATE")) {
			continue
		}
		if bytes.Contains(f.data, []byte("PRIVATE KEY")) {
			continue
		}
		buf.Write(f.data)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// configureWHCACert rebuilds the WekaHome CA bundle from the mounted secret, or leaves no
// bundle if the secret is absent or holds no usable certificate.
// Mirrors the WH CA block in Python configure_persistency() (weka_runtime.py:3243-3259).
func configureWHCACert() error {
	if err := os.RemoveAll(wekahomeCACertOutDir); err != nil {
		return fmt.Errorf("remove stale wh-cacert dir: %w", err)
	}

	entries, err := os.ReadDir(wekahomeCACertSecretDir)
	if err != nil {
		return nil // secret not mounted: matches `if [ -d ... ]`
	}

	var files []caCertFile
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "..") {
			continue // kubelet's ..data/..timestamp entries, not regular files
		}
		full := filepath.Join(wekahomeCACertSecretDir, name)
		info, err := os.Stat(full) // follows symlinks, like `[ -f "$f" ]`
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		files = append(files, caCertFile{name: name, data: data})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

	bundle := filterCACertContent(files)
	if !bytes.Contains(bundle, []byte("BEGIN CERTIFICATE")) {
		return nil // no usable certificate: leave no file, dir already removed
	}

	if err := os.MkdirAll(wekahomeCACertOutDir, 0o755); err != nil {
		return fmt.Errorf("mkdir wh-cacert dir: %w", err)
	}
	if err := os.WriteFile(wekahomeCACertOutFile, bundle, 0o400); err != nil {
		return fmt.Errorf("write wh-cacert bundle: %w", err)
	}
	return os.Chmod(wekahomeCACertOutFile, 0o400)
}
