package project

import (
	"github.com/ocelhq/ocel/cli/internal/clierror"
)

func newInvalidConfigError(err error, field string) error {
	return &clierror.Error{Code: clierror.CodeProjectInvalidConfig, Hint: field, Cause: err}
}

func newNoConfigError(err error, hint string) error {
	return &clierror.Error{Code: clierror.CodeProjectNoConfig, Hint: hint, Cause: err}
}
