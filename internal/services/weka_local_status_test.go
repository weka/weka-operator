package services

import (
	"encoding/json"
	"testing"
)

// TestIoProcessesNotUp pins the tolerance of the io_processes_not_up decode. The field's shape is
// set by the weka CLI, not by us: it is quoted, holds a comma-separated list of process ids
// ("15011, 15014"), and is reported as "" on STEM-mode and disabled containers. Decoding it into a
// fixed Go type would make any shape we did not anticipate fail the *entire* `weka local ps`
// unmarshal, and that error path in reconcileWekaLocalStatus can force a drivers reload — so the
// enclosing unmarshal succeeding is the property under test here, not just the returned value.
func TestIoProcessesNotUp(t *testing.T) {
	cases := []struct {
		name      string
		field     string // raw JSON for the io_processes_not_up member, "" to omit it entirely
		wantValue string
		wantNotUp bool
	}{
		{name: "process id list (what weka emits today)", field: `"15011, 15014"`, wantValue: "15011, 15014", wantNotUp: true},
		{name: "single process id", field: `"3"`, wantValue: "3", wantNotUp: true},
		{name: "empty string (STEM mode / disabled)", field: `""`, wantValue: "", wantNotUp: false},
		{name: "quoted zero", field: `"0"`, wantValue: "0", wantNotUp: false},
		{name: "null", field: `null`, wantValue: "", wantNotUp: false},
		{name: "field absent", field: "", wantValue: "", wantNotUp: false},
		{name: "unquoted (hypothetical future build)", field: `3`, wantValue: "3", wantNotUp: true},
		// Nothing is parsed, so an unrecognized shape blocks the upgrade gate rather than
		// failing open past a container we know nothing about.
		{name: "unexpected object", field: `{"up":1}`, wantValue: `{"up":1}`, wantNotUp: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload := `{"display_status":"READY","state":"READY"`
			if c.field != "" {
				payload += `,"io_processes_not_up":` + c.field
			}
			payload += `}`

			var status WekaLocalInternalStatus
			if err := json.Unmarshal([]byte(payload), &status); err != nil {
				t.Fatalf("unmarshal must never fail on this field, got %v for %s", err, payload)
			}

			// Unrelated fields must still decode - a tolerant field is worthless if the
			// shape variation quietly drops the rest of the struct.
			if status.DisplayStatus != "READY" {
				t.Errorf("display_status = %q, want READY", status.DisplayStatus)
			}

			value, notUp := status.IoProcessesNotUp()
			if value != c.wantValue {
				t.Errorf("value = %q, want %q", value, c.wantValue)
			}
			if notUp != c.wantNotUp {
				t.Errorf("notUp = %v, want %v", notUp, c.wantNotUp)
			}
		})
	}
}
