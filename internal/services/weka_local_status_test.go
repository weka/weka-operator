package services

import (
	"encoding/json"
	"testing"
)

// TestIoProcessesNotUpCount pins the tolerance of the io_processes_not_up decode. The field's shape
// is set by the weka CLI, not by us: it is quoted today ("3"), and reported as "" on STEM-mode and
// disabled containers. Decoding it into a fixed Go type would make any shape we did not anticipate
// fail the *entire* `weka local ps` unmarshal, and that error path in reconcileWekaLocalStatus can
// force a drivers reload — so the enclosing unmarshal succeeding is the property under test here,
// not just the parsed value.
func TestIoProcessesNotUpCount(t *testing.T) {
	three := 3

	cases := []struct {
		name    string
		field   string // raw JSON for the io_processes_not_up member, "" to omit it entirely
		want    *int
		wantErr bool
	}{
		{name: "quoted count (what weka emits today)", field: `"3"`, want: &three},
		{name: "empty string (STEM mode / disabled)", field: `""`, want: nil},
		{name: "unquoted count (hypothetical future build)", field: `3`, want: &three},
		{name: "null", field: `null`, want: nil},
		{name: "field absent", field: "", want: nil},
		{name: "quoted zero", field: `"0"`, want: new(int)},
		{name: "unparsable", field: `"n/a"`, want: nil, wantErr: true},
		{name: "unexpected object", field: `{"up":1}`, want: nil, wantErr: true},
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

			got, err := status.IoProcessesNotUpCount()
			if c.wantErr && err == nil {
				t.Errorf("want an error so the fail-open is visible in the log, got nil")
			}
			if !c.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			switch {
			case c.want == nil && got != nil:
				t.Errorf("count = %d, want nil", *got)
			case c.want != nil && got == nil:
				t.Errorf("count = nil, want %d", *c.want)
			case c.want != nil && got != nil && *c.want != *got:
				t.Errorf("count = %d, want %d", *got, *c.want)
			}
		})
	}
}
