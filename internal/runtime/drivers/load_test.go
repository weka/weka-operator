package drivers

import (
	"context"
	"errors"
	"testing"

	"github.com/weka/weka-operator/internal/runtime/process"
)

// fakeRunner records issued commands and returns scripted results/errors per binary path.
type fakeRunner struct {
	commands []process.Command
	errs     map[string]error
}

func (f *fakeRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	f.commands = append(f.commands, c)
	if err, ok := f.errs[c.Path]; ok {
		return process.Result{}, err
	}
	return process.Result{}, nil
}

func (f *fakeRunner) hasArg(path, arg string) bool {
	for _, c := range f.commands {
		if c.Path != path {
			continue
		}
		for _, a := range c.Args {
			if a == arg {
				return true
			}
		}
	}
	return false
}

// TestSetupOverlayfsForLibModules_SkipsWhenNotHostMount verifies the function returns early,
// issuing no mount commands, when /lib/modules is not a host mountpoint.
func TestSetupOverlayfsForLibModules_SkipsWhenNotHostMount(t *testing.T) {
	runner := &fakeRunner{errs: map[string]error{"mountpoint": errors.New("not a mountpoint")}}

	if err := SetupOverlayfsForLibModules(context.Background(), runner); err != nil {
		t.Fatalf("SetupOverlayfsForLibModules() error = %v", err)
	}

	if runner.hasArg("mount", "-t") {
		t.Errorf("expected no mount commands, got %+v", runner.commands)
	}
}

// TestLoadModules_IssuesArpTablesAndUioPciGeneric verifies both modules are requested via the
// runner when uio_pci_generic is not skipped.
func TestLoadModules_IssuesArpTablesAndUioPciGeneric(t *testing.T) {
	runner := &fakeRunner{}

	LoadModules(context.Background(), runner, false)

	if !runner.hasArg("modprobe", "arp_tables") {
		t.Errorf("expected arp_tables modprobe, got %+v", runner.commands)
	}
	if !runner.hasArg("modprobe", "uio_pci_generic") {
		t.Errorf("expected uio_pci_generic modprobe, got %+v", runner.commands)
	}
}

// TestLoadModules_SkipsUioPciGenericWhenRequested verifies skipUIOPCIGeneric suppresses that modprobe.
func TestLoadModules_SkipsUioPciGenericWhenRequested(t *testing.T) {
	runner := &fakeRunner{}

	LoadModules(context.Background(), runner, true)

	if runner.hasArg("modprobe", "uio_pci_generic") {
		t.Errorf("expected no uio_pci_generic modprobe, got %+v", runner.commands)
	}
	if !runner.hasArg("modprobe", "arp_tables") {
		t.Errorf("expected arp_tables modprobe, got %+v", runner.commands)
	}
}
