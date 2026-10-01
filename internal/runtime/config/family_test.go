package config

import "testing"

// Every family must parse successfully against an empty environment, matching Python's
// behavior of falling back to defaults rather than requiring every var to be set.
func TestFamiliesSucceedOnEmptyEnv(t *testing.T) {
	empty := Env{}
	if _, err := ParseContainer(empty); err != nil {
		t.Errorf("ParseContainer(empty) = %v", err)
	}
	if _, err := ParseClient(empty); err != nil {
		t.Errorf("ParseClient(empty) = %v", err)
	}
	if _, err := ParseContainerOp(empty); err != nil {
		t.Errorf("ParseContainerOp(empty) = %v", err)
	}
	if _, err := ParseAdhoc(empty); err != nil {
		t.Errorf("ParseAdhoc(empty) = %v", err)
	}
	if _, err := ParseDriverBuilder(empty); err != nil {
		t.Errorf("ParseDriverBuilder(empty) = %v", err)
	}
	if _, err := ParseDiscovery(empty); err != nil {
		t.Errorf("ParseDiscovery(empty) = %v", err)
	}
	if _, err := ParseDriverLoader(empty); err != nil {
		t.Errorf("ParseDriverLoader(empty) = %v", err)
	}
	if _, err := ParseRuntimeSection(empty); err != nil {
		t.Errorf("ParseRuntimeSection(empty) = %v", err)
	}
}

func TestFailureDomainAbsentVsEmpty(t *testing.T) {
	cfg, err := ParseContainer(Env{})
	if err != nil {
		t.Fatalf("ParseContainer(empty) = %v", err)
	}
	if cfg.Identity.FailureDomain != nil {
		t.Errorf("FailureDomain absent should be nil, got %v", *cfg.Identity.FailureDomain)
	}

	cfg, err = ParseContainer(Env{"FAILURE_DOMAIN": ""})
	if err != nil {
		t.Fatalf("ParseContainer(FAILURE_DOMAIN=\"\") = %v", err)
	}
	if cfg.Identity.FailureDomain == nil || *cfg.Identity.FailureDomain != "" {
		t.Errorf("FailureDomain explicit empty should be a pointer to \"\", got %v", cfg.Identity.FailureDomain)
	}
}

// Families without Operation must not fail on malformed INSTRUCTIONS, since they never decode it.
// Families with Operation dispatch on its type, so a malformed envelope is fatal there.
func TestMalformedInstructionsDeferredByFamily(t *testing.T) {
	badEnv := Env{"INSTRUCTIONS": "{not json"}

	if _, err := ParseContainer(badEnv); err != nil {
		t.Errorf("ParseContainer should ignore INSTRUCTIONS entirely, got %v", err)
	}
	if _, err := ParseClient(badEnv); err != nil {
		t.Errorf("ParseClient should ignore INSTRUCTIONS entirely, got %v", err)
	}

	if _, err := ParseContainerOp(badEnv); err == nil {
		t.Error("ParseContainerOp should fail on malformed INSTRUCTIONS")
	}
	if _, err := ParseAdhoc(badEnv); err == nil {
		t.Error("ParseAdhoc should fail on malformed INSTRUCTIONS")
	}
}

func TestDriverBuilderServePortRule(t *testing.T) {
	cfg, err := ParseDriverBuilder(Env{})
	if err != nil || cfg.ServePort != 60002 {
		t.Errorf("ParseDriverBuilder(empty) ServePort = (%d, %v), want (60002, nil)", cfg.ServePort, err)
	}

	cfg, err = ParseDriverBuilder(Env{"PORT": ""})
	if err != nil || cfg.ServePort != 60002 {
		t.Errorf("ParseDriverBuilder(PORT=\"\") ServePort = (%d, %v), want (60002, nil)", cfg.ServePort, err)
	}

	cfg, err = ParseDriverBuilder(Env{"PORT": "9000"})
	if err != nil || cfg.ServePort != 9000 {
		t.Errorf("ParseDriverBuilder(PORT=9000) ServePort = (%d, %v), want (9000, nil)", cfg.ServePort, err)
	}

	if _, err := ParseDriverBuilder(Env{"PORT": "nope"}); err == nil {
		t.Error("ParseDriverBuilder should fail on malformed PORT")
	}
}
