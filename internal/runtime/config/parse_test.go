package config

import "testing"

// Python's os.environ.get(KEY, default) applies the default only when the key is absent;
// an explicit "" must stay "" (py: CPU_POLICY 46, SYSLOG_PACKAGE 284, WEKA_PERSISTENCE_MODE 115,
// DUMPER_CONFIG_MODE 86, WEKA_COS_GLOBAL_HUGEPAGE_SIZE 123).
func TestAbsentVsEmptyDefaults(t *testing.T) {
	cpu, err := parseCPU(Env{})
	if err != nil || cpu.Policy != "auto" {
		t.Errorf("parseCPU absent CPU_POLICY = (%q, %v), want (auto, nil)", cpu.Policy, err)
	}
	cpu, err = parseCPU(Env{"CPU_POLICY": ""})
	if err != nil || cpu.Policy != "" {
		t.Errorf("parseCPU CPU_POLICY=\"\" = (%q, %v), want (\"\", nil)", cpu.Policy, err)
	}

	agent := parseAgent(Env{})
	if agent.SyslogPackage != "auto" {
		t.Errorf("parseAgent absent SYSLOG_PACKAGE = %q, want auto", agent.SyslogPackage)
	}
	agent = parseAgent(Env{"SYSLOG_PACKAGE": ""})
	if agent.SyslogPackage != "" {
		t.Errorf("parseAgent SYSLOG_PACKAGE=\"\" = %q, want \"\"", agent.SyslogPackage)
	}

	persistence := parsePersistence(Env{})
	if persistence.Mode != "local" {
		t.Errorf("parsePersistence absent WEKA_PERSISTENCE_MODE = %q, want local", persistence.Mode)
	}
	persistence = parsePersistence(Env{"WEKA_PERSISTENCE_MODE": ""})
	if persistence.Mode != "" {
		t.Errorf("parsePersistence WEKA_PERSISTENCE_MODE=\"\" = %q, want \"\"", persistence.Mode)
	}

	traces, err := parseTraces(Env{})
	if err != nil || traces.DumperConfigMode != "auto" {
		t.Errorf("parseTraces absent DUMPER_CONFIG_MODE = (%q, %v), want (auto, nil)", traces.DumperConfigMode, err)
	}
	traces, err = parseTraces(Env{"DUMPER_CONFIG_MODE": ""})
	if err != nil || traces.DumperConfigMode != "" {
		t.Errorf("parseTraces DUMPER_CONFIG_MODE=\"\" = (%q, %v), want (\"\", nil)", traces.DumperConfigMode, err)
	}

	host, err := parseHost(Env{})
	if err != nil || host.GlobalHugepageSize != "2M" {
		t.Errorf("parseHost absent WEKA_COS_GLOBAL_HUGEPAGE_SIZE = (%q, %v), want (2M, nil)", host.GlobalHugepageSize, err)
	}
	host, err = parseHost(Env{"WEKA_COS_GLOBAL_HUGEPAGE_SIZE": ""})
	if err != nil || host.GlobalHugepageSize != "" {
		t.Errorf("parseHost WEKA_COS_GLOBAL_HUGEPAGE_SIZE=\"\" = (%q, %v), want (\"\", nil)", host.GlobalHugepageSize, err)
	}
	host, err = parseHost(Env{"WEKA_COS_GLOBAL_HUGEPAGE_SIZE": "2G"})
	if err != nil || host.GlobalHugepageSize != "2g" {
		t.Errorf("parseHost WEKA_COS_GLOBAL_HUGEPAGE_SIZE=2G = (%q, %v), want (2g, nil)", host.GlobalHugepageSize, err)
	}
}

func TestExactBoolPolicy(t *testing.T) {
	cases := map[string]bool{"true": true, "True": false, "TRUE": false, "": false, "false": false}
	for v, want := range cases {
		if got := parseExactBool(Env{"K": v}, "K"); got != want {
			t.Errorf("parseExactBool(%q) = %v, want %v", v, got, want)
		}
	}
	if parseExactBool(Env{}, "K") != false {
		t.Error("parseExactBool on unset key should be false")
	}
}

func TestFoldBoolPolicy(t *testing.T) {
	if !parseFoldBool(Env{"K": "TRUE"}, "K", false) {
		t.Error("parseFoldBool should case-fold")
	}
	if !parseFoldBool(Env{}, "K", true) {
		t.Error("parseFoldBool should use default when unset")
	}
	if parseFoldBool(Env{"K": "false"}, "K", true) {
		t.Error("parseFoldBool should honor explicit false over default")
	}
}

func TestOptionalFoldBoolPolicy(t *testing.T) {
	if got := parseOptionalFoldBool(Env{}, "K"); got != nil {
		t.Errorf("parseOptionalFoldBool on unset key should be nil, got %v", got)
	}
	got := parseOptionalFoldBool(Env{"K": "TRUE"}, "K")
	if got == nil || !*got {
		t.Errorf("parseOptionalFoldBool(TRUE) = %v, want pointer to true", got)
	}
	got = parseOptionalFoldBool(Env{"K": ""}, "K")
	if got == nil || *got {
		t.Errorf("parseOptionalFoldBool(\"\") = %v, want pointer to false (set but not true)", got)
	}
}

func TestNotFalseBoolPolicy(t *testing.T) {
	if !parseNotFalse(Env{}, "K") {
		t.Error("parseNotFalse should default true when unset")
	}
	if !parseNotFalse(Env{"K": "anything"}, "K") {
		t.Error("parseNotFalse should be true for any non-false value")
	}
	if parseNotFalse(Env{"K": "false"}, "K") {
		t.Error("parseNotFalse should be false for explicit false")
	}
	if parseNotFalse(Env{"K": "FALSE"}, "K") {
		t.Error("parseNotFalse should case-fold false")
	}
}

func TestIntStrictPolicy(t *testing.T) {
	n, err := parseIntStrict(Env{}, "K", 7)
	if err != nil || n != 7 {
		t.Errorf("parseIntStrict unset = (%d, %v), want (7, nil)", n, err)
	}
	n, err = parseIntStrict(Env{"K": ""}, "K", 7)
	if err != nil || n != 7 {
		t.Errorf("parseIntStrict empty = (%d, %v), want (7, nil)", n, err)
	}
	n, err = parseIntStrict(Env{"K": "42"}, "K", 7)
	if err != nil || n != 42 {
		t.Errorf("parseIntStrict explicit = (%d, %v), want (42, nil)", n, err)
	}
	if _, err := parseIntStrict(Env{"K": "nope"}, "K", 7); err == nil {
		t.Error("parseIntStrict should error on malformed input")
	}
}

func TestPortPermissivePolicy(t *testing.T) {
	if got := parsePortPermissive(Env{}, "K"); got != 0 {
		t.Errorf("parsePortPermissive unset = %d, want 0", got)
	}
	if got := parsePortPermissive(Env{"K": "nope"}, "K"); got != 0 {
		t.Errorf("parsePortPermissive malformed = %d, want 0", got)
	}
	if got := parsePortPermissive(Env{"K": "1234"}, "K"); got != 1234 {
		t.Errorf("parsePortPermissive valid = %d, want 1234", got)
	}
}

func TestCSVPolicy(t *testing.T) {
	if got := parseCSV(Env{}, "K"); got != nil {
		t.Errorf("parseCSV unset = %v, want nil", got)
	}
	got := parseCSV(Env{"K": "a, b ,c"}, "K")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("parseCSV = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parseCSV[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCoreSelectionPolicy(t *testing.T) {
	sel, err := parseCoreSelection(Env{}, "K")
	if err != nil || !sel.Auto {
		t.Errorf("parseCoreSelection unset = (%v, %v), want Auto", sel, err)
	}
	sel, err = parseCoreSelection(Env{"K": "auto"}, "K")
	if err != nil || !sel.Auto {
		t.Errorf("parseCoreSelection(auto) = (%v, %v), want Auto", sel, err)
	}
	sel, err = parseCoreSelection(Env{"K": "2,0,1"}, "K")
	if err != nil || sel.Auto || len(sel.IDs) != 3 || sel.IDs[0] != 2 {
		t.Errorf("parseCoreSelection(2,0,1) = (%v, %v), want ordered explicit list", sel, err)
	}
	if _, err := parseCoreSelection(Env{"K": "nope"}, "K"); err == nil {
		t.Error("parseCoreSelection should error on malformed explicit list")
	}
}

func TestSelectorsPolicy(t *testing.T) {
	sel, err := parseSelectors(Env{}, "K")
	if err != nil || sel != nil {
		t.Errorf("parseSelectors unset = (%v, %v), want (nil, nil)", sel, err)
	}
	sel, err = parseSelectors(Env{"K": `[{"subnet":"10.0.0.0/8","min":1,"max":2}]`}, "K")
	if err != nil || len(sel) != 1 || sel[0].Subnet != "10.0.0.0/8" {
		t.Errorf("parseSelectors valid json = (%v, %v)", sel, err)
	}
	if _, err := parseSelectors(Env{"K": "{not json"}, "K"); err == nil {
		t.Error("parseSelectors should error on malformed json")
	}
}
