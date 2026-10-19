package adhoc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/process"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
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

func TestCollectRawDrives_FindDisksFailureReportsNotOK(t *testing.T) {
	runner := &fakeRunner{err: map[string]error{"nsenter": os.ErrInvalid}}
	drives, ok := collectRawDrives(context.Background(), runner)
	if ok || drives != nil {
		t.Errorf("collectRawDrives() = (%v, %v), want (nil, false)", drives, ok)
	}
}

type instantClock struct{}

func (instantClock) Now() time.Time { return time.Time{} }
func (instantClock) After(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch
}

// signToolRunner fakes /weka-sign-drive: `list -j` returns list, and a sign of exactly one path returns perDevice[path]. Any other sign (the batch) returns batchErr.
// Every other binary fails, so discovery reports no drives.
type signToolRunner struct {
	list      string
	batchErr  error
	perDevice map[string]process.Result
	signed    [][]string
}

func (r *signToolRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	if c.Path != "/weka-sign-drive" {
		return process.Result{}, os.ErrNotExist
	}
	switch c.Args[0] {
	case "list":
		return process.Result{Stdout: []byte(r.list)}, nil
	case "sign":
		paths := c.Args[slices.Index(c.Args, "--")+1:]
		r.signed = append(r.signed, paths)
		if len(paths) == 1 {
			if res, ok := r.perDevice[paths[0]]; ok {
				if res.ExitCode != 0 {
					return res, errors.New("exit status 1")
				}
				return res, nil
			}
		}
		return process.Result{}, r.batchErr
	}
	return process.Result{}, os.ErrNotExist
}

func runSignDrives(t *testing.T, runner process.CommandRunner, payload domain.SignedDrivesExtendedPayload) domain.DriveNodeResults {
	t.Helper()
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	resultsPath := filepath.Join(t.TempDir(), "results.json")
	if err = RunSignDrives(context.Background(), runner, instantClock{}, string(payloadJSON), resultsPath); err != nil {
		t.Fatalf("RunSignDrives() error = %v", err)
	}
	data, err := os.ReadFile(resultsPath)
	if err != nil {
		t.Fatal(err)
	}
	var res domain.DriveNodeResults
	if err = json.Unmarshal(data, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRunSignDrivesCountsFull(t *testing.T) {
	// a: annotated + cluster GUID + rule match -> already signed only. c: rule match -> excluded.
	// x, y: already signed / rule-matched but not candidates of this run -> not counted.
	// b: annotated serial without a cluster GUID -> signed again.
	runner := &signToolRunner{list: `{"devices":[
		{"path":"/dev/a","hardware":{"serial_number":"SA","model":"EXCL"},"weka_info":{"cluster_guid":"g"}},
		{"path":"/dev/b","hardware":{"serial_number":"SB"}},
		{"path":"/dev/c","hardware":{"serial_number":"SC","model":"EXCL"}},
		{"path":"/dev/x","hardware":{"serial_number":"SX"},"weka_info":{"cluster_guid":"g"}},
		{"path":"/dev/y","hardware":{"serial_number":"SY","model":"EXCL"}}]}`}
	res := runSignDrives(t, runner, domain.SignedDrivesExtendedPayload{
		SignDrivesPayload: weka.SignDrivesPayload{
			Type:            "device-paths",
			DevicePaths:     []string{"/dev/a", "/dev/b", "/dev/c", "/dev/d", "/dev/e"},
			DriveExclusions: &weka.DriveExclusions{Rules: []weka.DriveExclusionRule{{Model: "EXCL"}}},
		},
		ExcludedSerialIds: []string{"SA", "SB", "SX"},
	})

	if got, want := runner.signed, [][]string{{"/dev/b", "/dev/d", "/dev/e"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("signed paths = %v, want %v", got, want)
	}
	if res.Signed != 3 || res.AlreadySigned != 1 || res.Excluded != 1 {
		t.Errorf("counts = signed %d, alreadySigned %d, excluded %d; want 3, 1, 1", res.Signed, res.AlreadySigned, res.Excluded)
	}
}

func TestRunSignDrivesCountsSharedFallback(t *testing.T) {
	runner := &signToolRunner{
		list:     `{"devices":[]}`,
		batchErr: errors.New("batch failed"),
		perDevice: map[string]process.Result{
			"/dev/a": {},
			"/dev/b": {ExitCode: 1, Stderr: []byte("/dev/b is already a Weka partition")},
			"/dev/c": {ExitCode: 1, Stderr: []byte("some other failure")},
		},
	}
	res := runSignDrives(t, runner, domain.SignedDrivesExtendedPayload{
		SignDrivesPayload: weka.SignDrivesPayload{Type: "device-paths", DevicePaths: []string{"/dev/a", "/dev/b", "/dev/c"}, Shared: true},
	})

	if res.Signed != 1 || res.AlreadySigned != 1 || res.Excluded != 0 {
		t.Errorf("counts = signed %d, alreadySigned %d, excluded %d; want 1, 1, 0", res.Signed, res.AlreadySigned, res.Excluded)
	}
}

func TestRunSignDrivesCountsSharedBatch(t *testing.T) {
	runner := &signToolRunner{list: `{"devices":[]}`}
	res := runSignDrives(t, runner, domain.SignedDrivesExtendedPayload{
		SignDrivesPayload: weka.SignDrivesPayload{Type: "device-paths", DevicePaths: []string{"/dev/a", "/dev/b"}, Shared: true},
	})

	if res.Signed != 2 || res.AlreadySigned != 0 || res.Excluded != 0 {
		t.Errorf("counts = signed %d, alreadySigned %d, excluded %d; want 2, 0, 0", res.Signed, res.AlreadySigned, res.Excluded)
	}
}
