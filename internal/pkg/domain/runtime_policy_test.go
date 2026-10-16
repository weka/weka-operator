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
