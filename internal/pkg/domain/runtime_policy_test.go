package domain

import (
	"encoding/json"
	"testing"
)

func TestNeedsOperatorResources(t *testing.T) {
	for mode, want := range map[string]bool{
		"compute": true, "drive": true, "s3": true, "nfs": true,
		"smbw": true, "data-services": true, "envoy": true,
		"telemetry": true, "client": true, "ssdproxy": false,
		"drivers-dist": false, "drivers-builder": false, "drivers-loader": false,
		"adhoc-op": false, "adhoc-op-with-container": false, "discovery": false,
		"dist": false, "": false, "unknown": false,
	} {
		t.Run(mode, func(t *testing.T) {
			if got := NeedsOperatorResources(mode); got != want {
				t.Fatalf("NeedsOperatorResources(%q) = %v, want %v", mode, got, want)
			}
		})
	}
}

func TestShutdownInstructionsWireContract(t *testing.T) {
	value := ShutdownInstructions{AllowForceStop: true}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"allow_stop":false,"allow_force_stop":true}` {
		t.Fatalf("unexpected wire contract: %s", data)
	}
	var decoded ShutdownInstructions
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != value {
		t.Fatalf("round trip: %+v", decoded)
	}
}

// TestShutdownInstructionsMalformedIsNotApproved pins the contract that malformed
// on-disk instructions must never decode into an approved (allow_stop/allow_force_stop)
// value: the caller falls back to a fresh zero-value struct on unmarshal error.
func TestShutdownInstructionsMalformedIsNotApproved(t *testing.T) {
	ret := ShutdownInstructions{AllowStop: true, AllowForceStop: true}
	if err := json.Unmarshal([]byte("{not valid json"), &ret); err == nil {
		t.Fatal("expected unmarshal error for malformed JSON")
	}
	// Mirrors shutdown.GetShutdownInstructions: on unmarshal error, reset to zero value.
	ret = ShutdownInstructions{}
	if ret.AllowStop || ret.AllowForceStop {
		t.Fatalf("fallback value must not be approved: %+v", ret)
	}
}
