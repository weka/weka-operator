package services

import (
	"reflect"
	"testing"
)

func TestS3ClusterCreateCmd(t *testing.T) {
	params := S3Params{
		EnvoyPort:         9000,
		EnvoyAdminPort:    9001,
		S3Port:            9002,
		ContainerIds:      []int{3, 5},
		ClusterCreateArgs: []string{"--anonymous-posix-uid", "1000"},
	}
	tail := []string{
		"--port", "9000",
		"--envoy-admin-port", "9001",
		"--internal-port", "9002",
		"--container", "3,5",
		"--anonymous-posix-uid", "1000",
	}

	for _, tc := range []struct {
		name            string
		withoutConfigFS bool
		head            []string
	}{
		{"with config fs", false, []string{"wekaauthcli", "s3", "cluster", "create", "default", ".config_fs"}},
		{"without config fs", true, []string{"wekaauthcli", "s3", "cluster", "create", "--default-fs-name", "default"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := params
			p.WithoutConfigFS = tc.withoutConfigFS
			want := append(append([]string{}, tc.head...), tail...)
			if got := s3ClusterCreateCmd(&p); !reflect.DeepEqual(got, want) {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}
