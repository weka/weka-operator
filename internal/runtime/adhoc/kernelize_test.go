package adhoc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/runtime/process"
)

type scriptedRunner struct {
	res process.Result
	err error
	got []process.Command
}

func (s *scriptedRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	s.got = append(s.got, c)
	return s.res, s.err
}

const kernelizeJSON = `{"results":[{"pci_address":"0000:bd:00.0","success":true}],` +
	`"summary":{"total_candidates":4,"filtered_out_in_use":3,"recovered":1,"failed":0}}`

func runKernelizeForTest(t *testing.T, r *scriptedRunner) map[string]any {
	t.Helper()
	path := filepath.Join(t.TempDir(), "results.json")
	if err := RunKernelize(context.Background(), r, path); err != nil {
		t.Fatalf("RunKernelize: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRunKernelizeSuccess(t *testing.T) {
	r := &scriptedRunner{res: process.Result{Stdout: []byte(kernelizeJSON + "\n")}}
	m := runKernelizeForTest(t, r)
	if r.got[0].Path != "/weka-sign-drive" || len(r.got[0].Args) != 2 || r.got[0].Args[0] != "kernelize" || r.got[0].Args[1] != "-J" {
		t.Errorf("command = %+v", r.got[0])
	}
	if m["err"] != nil {
		t.Errorf("err = %v, want null", m["err"])
	}
	want := map[string]float64{"candidates": 4, "filtered_out_in_use": 3, "recovered": 1, "failed": 0}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	if _, ok := m["raw"].(map[string]any)["results"]; !ok {
		t.Errorf("raw missing results: %v", m["raw"])
	}
}

func TestRunKernelizeNonZeroExitWithJSON(t *testing.T) {
	r := &scriptedRunner{
		res: process.Result{Stdout: []byte(kernelizeJSON), ExitCode: 2},
		err: &process.ExecError{Kind: process.FailureExit, Err: errors.New("exit status 2")},
	}
	m := runKernelizeForTest(t, r)
	if m["err"] != "weka-sign-drive kernelize exited with code 2" {
		t.Errorf("err = %v", m["err"])
	}
	if m["recovered"] != float64(1) {
		t.Errorf("recovered = %v, want 1", m["recovered"])
	}
}

func TestRunKernelizeBadJSON(t *testing.T) {
	t.Run("stderr reported", func(t *testing.T) {
		r := &scriptedRunner{
			res: process.Result{Stdout: []byte("not json"), Stderr: []byte("boom\n"), ExitCode: 1},
			err: &process.ExecError{Kind: process.FailureExit, Err: errors.New("exit status 1")},
		}
		m := runKernelizeForTest(t, r)
		s, _ := m["err"].(string)
		if s == "" || len(m) != 1 {
			t.Errorf("result = %v, want only err", m)
		}
	})
	t.Run("exit code when no stderr", func(t *testing.T) {
		r := &scriptedRunner{res: process.Result{ExitCode: 3}, err: &process.ExecError{Kind: process.FailureExit, Err: errors.New("x")}}
		m := runKernelizeForTest(t, r)
		if s, _ := m["err"].(string); s == "" {
			t.Errorf("err missing: %v", m)
		}
	})
}

func TestRunKernelizeCancelled(t *testing.T) {
	r := &scriptedRunner{err: &process.ExecError{Kind: process.FailureCancelled, Err: context.Canceled}}
	if err := RunKernelize(context.Background(), r, filepath.Join(t.TempDir(), "r.json")); err == nil {
		t.Fatal("want error on cancellation")
	}
}

// The operator's KernelizeResult must decode what the runtime writes.
func TestRunKernelizeResultDecodesInOperator(t *testing.T) {
	r := &scriptedRunner{res: process.Result{Stdout: []byte(kernelizeJSON)}}
	path := filepath.Join(t.TempDir(), "results.json")
	if err := RunKernelize(context.Background(), r, path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var got operations.KernelizeResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Err != "" || got.Candidates != 4 || got.FilteredOutInUse != 3 || got.Recovered != 1 || got.Failed != 0 || len(got.Raw) == 0 {
		t.Errorf("decoded = %+v", got)
	}
}

func TestResolveDevicePathsBySerialsUnresolved(t *testing.T) {
	r := &scriptedRunner{res: process.Result{Stdout: []byte("")}}
	if _, err := resolveDevicePathsBySerials(context.Background(), r, []string{"SER1"}); err == nil {
		t.Fatal("want error for unresolved serial")
	}
}
