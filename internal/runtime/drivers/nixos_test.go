package drivers

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/weka/weka-operator/internal/runtime/process"
)

type nixosFakeRunner struct {
	scripts  []string
	gcc      string
	failStep string
}

func (f *nixosFakeRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	script := c.Args[len(c.Args)-1]
	f.scripts = append(f.scripts, script)
	if script == "set -e\ngcc -dumpversion" {
		return process.Result{Stdout: []byte(f.gcc + "\n")}, nil
	}
	if f.failStep != "" && strings.Contains(script, f.failStep) {
		return process.Result{}, &process.ExecError{Kind: process.FailureExit, Result: process.Result{Stderr: []byte("boom")}, Err: errors.New("exit status 1")}
	}
	return process.Result{}, nil
}

const nixosProcVersion = "Linux version 6.18.52 (nixbld@localhost) (gcc (GCC) 15.2.0, GNU ld) #1 SMP"

func TestPrepareNixosHostKernelSequence(t *testing.T) {
	t.Setenv("PATH", "/bin")
	r := &nixosFakeRunner{gcc: "15.2.0"}
	if err := prepareNixosHostKernel(context.Background(), r, "6.18.52", nixosProcVersion, "nixos-gcc15"); err != nil {
		t.Fatal(err)
	}
	wantContains := []string{
		"gcc -dumpversion",
		"missing host mount",
		"mount -t overlay overlay -o lowerdir=/nix/store:/host/nix/store /tmp/nix-store-union",
		`[ -f "/host/current-system/sw/lib/modules/6.18.52/build/Makefile" ]`,
		`[ -d "/host/current-system/kernel-modules/lib/modules/6.18.52" ]`,
		"ln -sfn",
		"ln -s \"$(readlink -f \"$e\")\"",
		`depmod -a "6.18.52"`,
	}
	if len(r.scripts) != len(wantContains) {
		t.Fatalf("got %d commands, want %d: %q", len(r.scripts), len(wantContains), r.scripts)
	}
	for i, w := range wantContains {
		if !strings.Contains(r.scripts[i], w) {
			t.Errorf("command %d = %q, want it to contain %q", i, r.scripts[i], w)
		}
	}
	if !containsPathEntry(os.Getenv("PATH"), "/usr/bin") || !containsPathEntry(os.Getenv("PATH"), "/usr/local/bin") {
		t.Errorf("PATH = %q, want /usr/bin and /usr/local/bin added", os.Getenv("PATH"))
	}
}

func TestPrepareNixosHostKernelGccMismatch(t *testing.T) {
	t.Setenv("PATH", "/bin")
	r := &nixosFakeRunner{gcc: "14.2.0"}
	err := prepareNixosHostKernel(context.Background(), r, "6.18.52", nixosProcVersion, "nixos-gcc15")
	if err == nil || !strings.Contains(err.Error(), "builder image gcc 14 does not match kernel gcc 15") {
		t.Fatalf("err = %v", err)
	}
	if len(r.scripts) != 1 {
		t.Errorf("ran %d commands after mismatch, want only the gcc probe", len(r.scripts))
	}
}

func TestPrepareNixosHostKernelStepFailure(t *testing.T) {
	t.Setenv("PATH", "/bin")
	r := &nixosFakeRunner{gcc: "15", failStep: "depmod"}
	err := prepareNixosHostKernel(context.Background(), r, "6.18.52", nixosProcVersion, "nixos-gcc15")
	if err == nil || !strings.Contains(err.Error(), "running depmod") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}
