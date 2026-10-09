package runtimes

import "context"

// TaskRuntime runs a single bounded host operation and holds no container: discovery and
// drivers-loader. Start runs fn to completion; Shutdown is a no-op since there
// is nothing left running by the time it could be called.
type TaskRuntime struct {
	name string
	fn   func(context.Context) error
}

// NewTask wraps fn as a lifecycle.ModeRuntime.
func NewTask(name string, fn func(context.Context) error) *TaskRuntime {
	return &TaskRuntime{name: name, fn: fn}
}

func (t *TaskRuntime) Start(ctx context.Context) error {
	return t.fn(ctx)
}

func (t *TaskRuntime) Shutdown(context.Context) error {
	return nil
}
