package wekacontainer

import "testing"

func TestFindInterfaceByIP(t *testing.T) {
	out := `1: lo    inet 127.0.0.1/8 scope host lo
2: enp0s9    inet 10.0.136.214/18 scope global enp0s9
2: enp0s9    inet6 fd00::5/64 scope global
`
	tests := []struct {
		name    string
		output  string
		ip      string
		want    string
		wantErr bool
	}{
		{name: "primary interface", output: out, ip: "10.0.136.214", want: "enp0s9"},
		{name: "ipv6 address", output: out, ip: "fd00::5", want: "enp0s9"},
		{name: "non-canonical ipv6 address", output: out, ip: "fd00:0:0:0:0:0:0:5", want: "enp0s9"},
		{name: "invalid ip", output: out, ip: "not-an-ip", wantErr: true},
		{name: "no match", output: out, ip: "10.0.136.2", wantErr: true},
		{name: "empty output", output: "", ip: "10.0.136.214", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findInterfaceByIP(tt.output, tt.ip)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
