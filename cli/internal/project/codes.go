package project

import (
	"github.com/ocelhq/ocel/cli/internal/clierror"
)

const (
	codeNoConfig      = "project.no_config"
	codeInvalidConfig = "project.invalid_config"
)

func newInvalidConfigError(err error, field string) error {
	return &clierror.Error{Code: codeInvalidConfig, Hint: field, Cause: err}
}

func newNoConfigError(err error, hint string) error {
	return &clierror.Error{Code: codeNoConfig, Hint: hint, Cause: err}
}
