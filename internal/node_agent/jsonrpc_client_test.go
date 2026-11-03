package node_agent

import "testing"

func TestIsException(t *testing.T) {
	tests := []struct {
		data string
		want bool
	}{
		{data: `{"virtualUuid":"v","exceptionClass":["VirtualDriveNotFoundException","OperationFailedException"]}`, want: true},
		{data: `{"exceptionClass":["VirtualDriveInUseException","BadStateException"]}`},
		{data: `{"exceptionClass":"VirtualDriveNotFoundException"}`},
		{data: `"no space"`},
		{},
	}
	for _, tt := range tests {
		e := JSONRPCError{Data: []byte(tt.data)}
		if got := e.IsException("VirtualDriveNotFoundException"); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.data, got, tt.want)
		}
	}
}
