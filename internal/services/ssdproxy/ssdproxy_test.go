package ssdproxy

import "testing"

func TestParseAddVirtualDriveReply(t *testing.T) {
	tests := []struct {
		body    string
		wantVID string
		wantErr bool
	}{
		{body: `{"jsonrpc":"2.0","id":1,"result":"vid-1"}`, wantVID: "vid-1"},
		{body: `{"result":null,"error":{"code":-32000,"message":"no space"}}`, wantErr: true},
		{body: `{"result":""}`, wantErr: true},
		{body: `{"result":true}`, wantErr: true},
		{body: `not json`, wantErr: true},
	}
	for _, tt := range tests {
		vid, err := parseAddVirtualDriveReply([]byte(tt.body))
		if (err != nil) != tt.wantErr || vid != tt.wantVID {
			t.Errorf("%s: got (%q, %v), want vid %q, err %v", tt.body, vid, err, tt.wantVID, tt.wantErr)
		}
	}
}
