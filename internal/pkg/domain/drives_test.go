package domain

import (
	"encoding/json"
	"testing"
)

func TestParseDriveEntries_NewFormat(t *testing.T) {
	entries := []DriveEntry{
		{Serial: "SERIAL1", CapacityGiB: 500},
		{Serial: "SERIAL2", CapacityGiB: 1000},
	}
	data, _ := json.Marshal(entries)

	result, isOld, err := ParseDriveEntries(string(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isOld {
		t.Fatal("expected isOldFormat=false for new format")
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
	if result[0].Serial != "SERIAL1" || result[0].CapacityGiB != 500 {
		t.Errorf("unexpected first entry: %+v", result[0])
	}
	if result[1].Serial != "SERIAL2" || result[1].CapacityGiB != 1000 {
		t.Errorf("unexpected second entry: %+v", result[1])
	}
}

func TestParseDriveEntries_OldFormat(t *testing.T) {
	serials := []string{"SERIAL1", "SERIAL2", "SERIAL3"}
	data, _ := json.Marshal(serials)

	result, isOld, err := ParseDriveEntries(string(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isOld {
		t.Fatal("expected isOldFormat=true for old format")
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(result))
	}
	for i, entry := range result {
		if entry.Serial != serials[i] {
			t.Errorf("entry %d: expected serial %q, got %q", i, serials[i], entry.Serial)
		}
		if entry.CapacityGiB != 0 {
			t.Errorf("entry %d: expected CapacityGiB=0, got %d", i, entry.CapacityGiB)
		}
	}
}

func TestParseDriveEntries_OldFormatSkipsEmpty(t *testing.T) {
	serials := []string{"SERIAL1", "", "SERIAL3"}
	data, _ := json.Marshal(serials)

	result, isOld, err := ParseDriveEntries(string(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isOld {
		t.Fatal("expected isOldFormat=true")
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 entries (empty skipped), got %d", len(result))
	}
}

func TestParseDriveEntries_Empty(t *testing.T) {
	result, isOld, err := ParseDriveEntries("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isOld {
		t.Fatal("expected isOldFormat=false for empty")
	}
	if result != nil {
		t.Fatalf("expected nil result for empty, got %+v", result)
	}
}

func TestParseDriveEntries_InvalidJSON(t *testing.T) {
	_, _, err := ParseDriveEntries("not-json")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseDriveEntries_EmptyArray(t *testing.T) {
	result, isOld, err := ParseDriveEntries("[]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isOld {
		t.Fatal("expected isOldFormat=false for empty array")
	}
	if len(result) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(result))
	}
}

func TestDriveEntrySerials(t *testing.T) {
	entries := []DriveEntry{
		{Serial: "A", CapacityGiB: 100},
		{Serial: "B", CapacityGiB: 200},
	}
	serials := DriveEntrySerials(entries)
	if len(serials) != 2 || serials[0] != "A" || serials[1] != "B" {
		t.Errorf("unexpected serials: %v", serials)
	}
}

func TestDriveEntrySerials_Empty(t *testing.T) {
	serials := DriveEntrySerials(nil)
	if len(serials) != 0 {
		t.Errorf("expected empty, got %v", serials)
	}
}
