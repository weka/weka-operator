package utils

import "testing"

func TestGetNumericVersion(t *testing.T) {
	tests := []struct {
		image string
		want  string
	}{
		{
			image: "10.200.6.131:5000/weka-in-container:4.4.2.163-k8s-qa",
			want:  "4.4.2.163",
		},
		{
			image: "quay.io/weka.io/weka-in-container:4.4.2.163-k8s-qa",
			want:  "4.4.2.163",
		},
		{
			image: "quay.io/weka.io/weka-in-container:4.4.2",
			want:  "4.4.2",
		},
		{
			image: "image:4.4.2",
			want:  "4.4.2",
		},
		{
			image: "image:4.4.2.163",
			want:  "4.4.2.163",
		},
		{
			image: "image:4.4.2-rc1",
			want:  "4.4.2",
		},
		{
			image: "invalid-format",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			got := GetSoftwareVersion(tt.image)
			if got != tt.want {
				t.Errorf("GetSoftwareVersion(%q) = %q, want %q", tt.image, got, tt.want)
			}
		})
	}
}
