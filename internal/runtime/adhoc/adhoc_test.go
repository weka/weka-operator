package adhoc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// fakeRunner returns a scripted result per binary path, matching the pattern used
// across other migrated packages (see blockdev_test.go).
type fakeRunner struct {
	stdout map[string][]byte
	err    map[string]error
	calls  []process.Command
}

func (f *fakeRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.calls = append(f.calls, c)
	if err, ok := f.err[c.Path]; ok {
		return process.Result{}, err
	}
	return process.Result{Stdout: f.stdout[c.Path]}, nil
}

func TestPciToDevicePaths(t *testing.T) {
	runner := &fakeRunner{stdout: map[string][]byte{
		"lspci": []byte("0000:00:1f.0 Non-Volatile memory controller: Amazon.com, Inc. Device cd01\n0000:00:20.0 Non-Volatile memory controller: Amazon.com, Inc. Device cd01\n"),
	}}
	paths, err := pciToDevicePaths(context.Background(), runner, awsVendorID, awsDeviceID)
	if err != nil {
		t.Fatalf("pciToDevicePaths() error = %v", err)
	}
	want := []string{
		"/dev/disk/by-path/pci-0000:00:1f.0-nvme-1",
		"/dev/disk/by-path/pci-0000:00:20.0-nvme-1",
	}
	if len(paths) != len(want) {
		t.Fatalf("pciToDevicePaths() = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("path[%d] = %q, want %q", i, paths[i], want[i])
		}
	}
}

func TestPciToDevicePathsNoMatches(t *testing.T) {
	runner := &fakeRunner{err: map[string]error{"lspci": context.DeadlineExceeded}}
	paths, err := pciToDevicePaths(context.Background(), runner, awsVendorID, awsDeviceID)
	if err != nil {
		t.Fatalf("pciToDevicePaths() error = %v, want nil (no devices found is not an error)", err)
	}
	if paths != nil {
		t.Errorf("pciToDevicePaths() = %v, want nil", paths)
	}
}

func TestPciToDevicePathsMissingIDs(t *testing.T) {
	runner := &fakeRunner{}
	if _, err := pciToDevicePaths(context.Background(), runner, "", awsDeviceID); err == nil {
		t.Error("pciToDevicePaths() with empty vendorID: want error, got nil")
	}
}

func TestEnumerateDevicePaths(t *testing.T) {
	runner := &fakeRunner{stdout: map[string][]byte{
		"lspci": []byte("0000:00:1f.0 Non-Volatile memory controller: Amazon.com, Inc. Device cd01\n"),
	}}

	t.Run("device-paths passes through", func(t *testing.T) {
		payload := &domain.SignedDrivesExtendedPayload{Type: "device-paths", DevicePaths: []string{"/dev/nvme0n1"}}
		got, err := enumerateDevicePaths(context.Background(), runner, payload)
		if err != nil || len(got) != 1 || got[0] != "/dev/nvme0n1" {
			t.Errorf("enumerateDevicePaths() = %v, %v", got, err)
		}
	})

	t.Run("aws-all resolves via lspci", func(t *testing.T) {
		payload := &domain.SignedDrivesExtendedPayload{Type: "aws-all"}
		got, err := enumerateDevicePaths(context.Background(), runner, payload)
		if err != nil || len(got) != 1 {
			t.Errorf("enumerateDevicePaths() = %v, %v", got, err)
		}
	})

	t.Run("device-identifiers requires pciDevices", func(t *testing.T) {
		payload := &domain.SignedDrivesExtendedPayload{Type: "device-identifiers"}
		if _, err := enumerateDevicePaths(context.Background(), runner, payload); err == nil {
			t.Error("want error when PCIDevices is nil, got nil")
		}
	})

	t.Run("unknown type errors", func(t *testing.T) {
		payload := &domain.SignedDrivesExtendedPayload{Type: "bogus"}
		if _, err := enumerateDevicePaths(context.Background(), runner, payload); err == nil {
			t.Error("want error for unknown type, got nil")
		}
	})
}

func TestRunUmountParsesMountLinesAndRemovesModule(t *testing.T) {
	runner := &fakeRunner{stdout: map[string][]byte{
		"nsenter": []byte("wekafs on /mnt/weka type wekafs (rw)\n"),
	}}
	resultsPath := filepath.Join(t.TempDir(), "result.json")
	_ = RunUmount(context.Background(), runner, resultsPath)

	var umountCalls, rmmodCalls int
	for _, c := range runner.calls {
		if len(c.Args) < 2 {
			continue
		}
		last := c.Args[len(c.Args)-1]
		switch {
		case last == "/mnt/weka":
			umountCalls++
		case last == "wekafsio":
			rmmodCalls++
		}
	}
	if umountCalls != 1 {
		t.Errorf("umount calls = %d, want 1", umountCalls)
	}
	if rmmodCalls != 1 {
		t.Errorf("rmmod calls = %d, want 1 (only runs when no umount errors)", rmmodCalls)
	}
}

func TestRunEnsureNICsHelperFallsBackWithoutIRSA(t *testing.T) {
	runner := &fakeRunner{stdout: map[string][]byte{"weka": []byte(`{"metadata":{"vnics":[]}}`)}}
	// No token file present on disk -> tokenPresent is false -> falls back to plain invocation.
	out, err := runEnsureNICsHelper(context.Background(), runner, config.AWS{RoleARN: "arn:aws:iam::1:role/x", WebIdentityTokenFile: "/nonexistent/token"}, "cloud-helper ensure-nics -n 1")
	if err != nil {
		t.Fatalf("runEnsureNICsHelper() error = %v", err)
	}
	if string(out) != `{"metadata":{"vnics":[]}}` {
		t.Errorf("runEnsureNICsHelper() = %q", out)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(runner.calls))
	}
	if runner.calls[0].Stdin != nil {
		t.Error("fallback path must not set Stdin")
	}
}

func TestRunEnsureNICsHelperStreamsTokenViaStdin(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("secret-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{stdout: map[string][]byte{"weka": []byte(`{"metadata":{"vnics":[]}}`)}}

	_, err := runEnsureNICsHelper(context.Background(), runner, config.AWS{RoleARN: "arn:aws:iam::1:role/x", WebIdentityTokenFile: tokenFile}, "cloud-helper ensure-nics -n 1")
	if err != nil {
		t.Fatalf("runEnsureNICsHelper() error = %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(runner.calls))
	}
	c := runner.calls[0]
	if c.Stdin == nil {
		t.Error("IRSA path must stream token via Stdin")
	}
	if c.Log != process.LogNone {
		t.Errorf("Log = %v, want LogNone so the token never hits logs", c.Log)
	}
	for _, a := range c.Args {
		if a == "secret-token" {
			t.Error("token must not appear in Args")
		}
	}
}
