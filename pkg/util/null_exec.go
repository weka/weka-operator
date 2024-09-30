package util

import (
	"bytes"
	"context"
)

var _ Exec = (*nullExec)(nil)

func NewNullExec() *nullExec {
	return &nullExec{}
}

type nullExec struct{}

func (e *nullExec) Exec(ctx context.Context, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error) {
	return bytes.Buffer{}, bytes.Buffer{}, nil
}

func (e *nullExec) ExecNamed(ctx context.Context, name string, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error) {
	resultFn, ok := cmdStubs[name]
	if !ok {
		return bytes.Buffer{}, bytes.Buffer{}, nil // Default to no-op
	}
	result := resultFn()
	return *bytes.NewBufferString(result), bytes.Buffer{}, nil
}

func (e *nullExec) ExecSensitive(ctx context.Context, name string, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error) {
	resultFn, ok := cmdStubs[name]
	if !ok {
		return bytes.Buffer{}, bytes.Buffer{}, nil // Default to no-op
	}
	result := resultFn()
	return *bytes.NewBufferString(result), bytes.Buffer{}, nil
}

var cmdStubs = map[string]func() string{
	"AddClusterUser": func() string {
		return ""
	},
	"GenerateJoinSecret": func() string {
		return `"abc123"`
	},
	"GetManagementIpAddress": func() string {
		return "1.2.3.4"
	},
	"GetWekaStatus": func() string {
		return `{
			"status": "READY",
			"capacity": {
				"unprovisioned_bytes": 1000000000000,
				"total_bytes": 10000000000000
			}
		}`
	},
	"WekaLocalContainerGetIdentity": func() string {
		return `{
			"value": {
				"cluster_guid": "abc123",
				"host_id": 1
			}
		}`
	},
	"WekaLocalPs": func() string {
		return `[
			{
				"name": "test-container",
				"runStatus": "Running",
				"lastFailureText": ""
			}
		]`
	},
	"WekaListUsers": func() string {
		return "[]"
	},
}
