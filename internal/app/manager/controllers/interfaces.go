package controllers

import (
	"bytes"
	"context"
)

type Executor interface {
	Exec(ctx context.Context, command []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error)
}
