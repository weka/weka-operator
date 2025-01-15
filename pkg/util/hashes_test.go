package util

import (
	"testing"
)

type TestStruct struct {
	Name    string
	Age     int
	Details *HashableMap
}

func assertEqual(t *testing.T, actual, expected any, msg string) {
	if actual != expected {
		t.Errorf("%s: expected %v, got %v", msg, expected, actual)
	}
}

func assertNoError(t *testing.T, err error, msg string) {
	if err != nil {
		t.Errorf("%s: expected no error, got %v", msg, err)
	}
}

func assertNotEmpty(t *testing.T, value string, msg string) {
	if value == "" {
		t.Errorf("%s: expected non-empty value, got an empty string", msg)
	}
}

func TestHashStruct(t *testing.T) {
	tests := []struct {
		name         string
		input        any
		expectedHash string
		expectError  bool
	}{
		{
			name: "Simple struct",
			input: struct {
				Field1 string
				Field2 int
			}{
				Field1: "test",
				Field2: 42,
			},
			expectedHash: "2f3aedb61992e9c3c50190f6b9dfad65f1a637861a0084b9876dd294a03fcb8e",
			expectError:  false,
		},
		{
			name: "Struct with map (alphabetical order)",
			input: TestStruct{
				Name: "Alice",
				Age:  30,
				Details: NewHashableMap(map[string]string{
					"key1": "value1",
					"key2": "value2",
					"key3": "value3",
				}),
			},
			expectedHash: "0c50016326f371669fe4fec10128512e0776e2e8c226b03b684e10ea2ea8a88e",
			expectError:  false,
		},
		{
			name: "Struct with map (different order)",
			input: TestStruct{
				Name: "Alice",
				Age:  30,
				Details: NewHashableMap(map[string]string{
					"key3": "value3",
					"key2": "value2",
					"key1": "value1",
				}),
			},
			expectedHash: "0c50016326f371669fe4fec10128512e0776e2e8c226b03b684e10ea2ea8a88e",
			expectError:  false,
		},
		{
			name:         "Nil input",
			input:        nil,
			expectedHash: "",
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash, err := HashStruct(tt.input)
			if tt.expectError {
				if err == nil {
					t.Errorf("%s: expected an error but got nil", tt.name)
				}
				return
			}

			assertNoError(t, err, "Unexpected error for "+tt.name)
			assertNotEmpty(t, hash, "Hash should not be empty for "+tt.name)
			assertEqual(t, hash, tt.expectedHash, "Hash mismatch for "+tt.name)
		})
	}
}

func TestHashableMapEquals(t *testing.T) {
	tests := []struct {
		name     string
		input    *HashableMap
		other    *HashableMap
		expected bool
	}{
		{
			name:     "Equal maps",
			input:    NewHashableMap(map[string]string{"key1": "value1", "key2": "value2"}),
			other:    NewHashableMap(map[string]string{"key1": "value1", "key2": "value2"}),
			expected: true,
		},
		{
			name:     "Different maps",
			input:    NewHashableMap(map[string]string{"key1": "value1", "key2": "value2"}),
			other:    NewHashableMap(map[string]string{"key1": "value1", "key2": "value3"}),
			expected: false,
		},
		{
			name:     "Different lengths",
			input:    NewHashableMap(map[string]string{"key1": "value1", "key2": "value2"}),
			other:    NewHashableMap(map[string]string{"key1": "value1"}),
			expected: false,
		},
		{
			name:     "Same map, different order",
			input:    NewHashableMap(map[string]string{"key1": "value1", "key2": "value2"}),
			other:    NewHashableMap(map[string]string{"key2": "value2", "key1": "value1"}),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertEqual(t, tt.input.Equals(tt.other), tt.expected, "Equals mismatch for "+tt.name)
		})
	}
}
