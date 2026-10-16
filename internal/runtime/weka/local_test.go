package weka

import (
	"context"

	"github.com/weka/weka-operator/internal/runtime/process"
)

// stubRunner is a fake process.CommandRunner shared by this package's tests. It records every
// command it's asked to run, in order, and returns scripted results/errors by call index,
// holding the last scripted entry for any call past the end of the list.
type stubRunner struct {
	calls   []process.Command
	results []process.Result
	errs    []error
}

func (s *stubRunner) Run(_ context.Context, c process.Command) (process.Result, error) {
	i := len(s.calls)
	s.calls = append(s.calls, c)

	var res process.Result
	if i < len(s.results) {
		res = s.results[i]
	} else if len(s.results) > 0 {
		res = s.results[len(s.results)-1]
	}

	var err error
	if i < len(s.errs) {
		err = s.errs[i]
	} else if len(s.errs) > 0 {
		err = s.errs[len(s.errs)-1]
	}

	return res, err
}
