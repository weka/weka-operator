package main

import (
	"testing"

	flags "github.com/jessevdk/go-flags"
)

// parseOpts parses args into a fresh options struct using the real go-flags tags, so these tests exercise
// the actual flag definitions (defaults, choices, short names) rather than a copy.
func parseOpts(t *testing.T, args []string) options {
	t.Helper()
	var o options
	p := flags.NewParser(&o, flags.None)
	p.SubcommandsOptional = true // let the global-flag cases parse without naming a subcommand
	// Populate the struct but do NOT run the command's Execute (it would need a live cluster).
	p.CommandHandler = func(_ flags.Commander, _ []string) error { return nil }
	if _, err := p.ParseArgs(args); err != nil {
		t.Fatalf("ParseArgs(%v): unexpected error: %v", args, err)
	}
	return o
}

// TestNamespaceFlags asserts BUG A's fix: --namespace (cluster) and --operator-namespace (scrape) are
// independent, and --operator-namespace defaults to the operator's home so a cross-namespace cluster works
// out of the box.
func TestNamespaceFlags(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantNS     string
		wantOperNS string
	}{
		{"defaults", nil, "weka-operator-system", "weka-operator-system"},
		{"cluster ns only (-n)", []string{"-n", "default"}, "default", "weka-operator-system"},
		{"cluster ns only (--namespace)", []string{"--namespace", "default"}, "default", "weka-operator-system"},
		{"scrape ns override", []string{"-n", "default", "--operator-namespace", "ops"}, "default", "ops"},
		{"scrape ns only", []string{"--operator-namespace", "ops"}, "weka-operator-system", "ops"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := parseOpts(t, tc.args)
			if o.Namespace != tc.wantNS {
				t.Errorf("Namespace = %q, want %q", o.Namespace, tc.wantNS)
			}
			if o.OperatorNamespace != tc.wantOperNS {
				t.Errorf("OperatorNamespace = %q, want %q", o.OperatorNamespace, tc.wantOperNS)
			}
		})
	}
}

// TestFromOperatorFlagParsing asserts BUG B's fix: the scrape can actually be turned off. Both documented
// syntaxes (--from-operator=false and --from-operator false) must parse, default must be scrape-ON, and an
// invalid value must be rejected by the choice constraint.
func TestFromOperatorFlagParsing(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantVal    string
		wantScrape bool
	}{
		{"default is on", []string{"plan", "--cluster", "x"}, "true", true},
		{"equals-false disables", []string{"plan", "--cluster", "x", "--from-operator=false"}, "false", false},
		{"space-false disables", []string{"plan", "--cluster", "x", "--from-operator", "false"}, "false", false},
		{"equals-true enables", []string{"plan", "--cluster", "x", "--from-operator=true"}, "true", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := parseOpts(t, tc.args)
			got := o.Plan.Constraints.FromOperator
			if got != tc.wantVal {
				t.Fatalf("FromOperator = %q, want %q", got, tc.wantVal)
			}
			if scrapeEnabled(&o.Plan.Constraints) != tc.wantScrape {
				t.Errorf("scrapeEnabled(%q) = %v, want %v", got, scrapeEnabled(&o.Plan.Constraints), tc.wantScrape)
			}
		})
	}

	// An out-of-choice value must fail to parse (proving the toggle is validated, not silently accepted).
	var o options
	p := flags.NewParser(&o, flags.None)
	p.SubcommandsOptional = true
	p.CommandHandler = func(_ flags.Commander, _ []string) error { return nil }
	if _, err := p.ParseArgs([]string{"plan", "--cluster", "x", "--from-operator=maybe"}); err == nil {
		t.Errorf("--from-operator=maybe: expected a choice-validation error, got nil")
	}
}

// TestScrapeEnabled covers the gate directly, including the zero-value literal (empty string) used by other
// unit tests that build constraintFlags without setting FromOperator — it must default to scrape-ON.
func TestScrapeEnabled(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", true},       // zero-value literal ⇒ on (matches the flag default)
		{"true", true},   // explicit on
		{"false", false}, // explicit off
	}
	for _, tc := range cases {
		if got := scrapeEnabled(&constraintFlags{FromOperator: tc.val}); got != tc.want {
			t.Errorf("scrapeEnabled(FromOperator=%q) = %v, want %v", tc.val, got, tc.want)
		}
	}
}
