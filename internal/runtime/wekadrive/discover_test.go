package wekadrive

import "testing"

func TestDriveSignatureInfo(t *testing.T) {
	tests := []struct {
		name       string
		signature  string
		wantSigned bool
		wantGUID   string
	}{
		{
			name:       "empty string — unsigned, no GUID",
			signature:  "",
			wantSigned: false,
			wantGUID:   "",
		},
		{
			name:       "unsignedDriveSignature constant — unsigned, no GUID",
			signature:  unsignedDriveSignature,
			wantSigned: false,
			wantGUID:   "",
		},
		{
			name:       "valid 32-hex-char — signed, correctly dashed 8-4-4-4-12 GUID",
			signature:  "90f0090f90f0090f90f0090f90f0090e", // differs from unsigned by last char
			wantSigned: true,
			wantGUID:   "90f0090f-90f0-090f-90f0-090f90f0090e",
		},
		{
			name:       "another valid 32-hex signature",
			signature:  "aabbccdd11223344aabbccdd11223344",
			wantSigned: true,
			wantGUID:   "aabbccdd-1122-3344-aabb-ccdd11223344",
		},
		{
			name:       "signed but wrong length (30 chars) — signed, empty GUID",
			signature:  "90f0090f90f0090f90f0090f90f009", // 30 chars, not unsigned
			wantSigned: true,
			wantGUID:   "",
		},
		{
			name:       "signed but wrong length (34 chars) — signed, empty GUID",
			signature:  "90f0090f90f0090f90f0090f90f0090f00", // 34 chars
			wantSigned: true,
			wantGUID:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSigned, gotGUID := driveSignatureInfo(tt.signature)
			if gotSigned != tt.wantSigned {
				t.Errorf("driveSignatureInfo(%q) isSigned = %v; want %v", tt.signature, gotSigned, tt.wantSigned)
			}
			if gotGUID != tt.wantGUID {
				t.Errorf("driveSignatureInfo(%q) wekaGUID = %q; want %q", tt.signature, gotGUID, tt.wantGUID)
			}
		})
	}
}
