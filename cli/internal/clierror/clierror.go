package clierror

import (
	"errors"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/docsurl"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

const (
	internalCode                   = "internal"
	CodeConfirmationRequired       = "confirmation_required"
	CodeConfirmationBypassMismatch = "confirmation_bypass_mismatch"
	CodeInputRequired              = "input_required"
)

type Error struct {
	Code      string
	Message   string
	Hint      string
	Retryable bool
	Cause     error
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return e.Code
	}
	return e.Cause.Error()
}

func (e *Error) Unwrap() error { return e.Cause }

func NewRunError(err error) *streamv1.RunError {
	if err == nil {
		return nil
	}
	runError := &streamv1.RunError{Code: internalCode, Message: err.Error()}
	var coded *Error
	if !errors.As(err, &coded) || coded == nil {
		return runError
	}
	if coded.Message != "" {
		runError.Message = coded.Message
	}
	runError.Retryable = coded.Retryable
	if coded.Hint != "" {
		runError.Hint = &coded.Hint
	}
	if isPublishedCode(coded.Code) {
		runError.Code = coded.Code
		runError.DocsUrl = proto.String(docsurl.FormatErrorPage(coded.Code))
	}
	return runError
}

func NewConfirmationRequired(cause error, typedInput string) *Error {
	return &Error{Code: CodeConfirmationRequired, Hint: typedInput, Cause: cause}
}

func NewConfirmationBypassMismatch(cause error) *Error {
	return &Error{Code: CodeConfirmationBypassMismatch, Cause: cause}
}

func NewInputRequired(cause error, typedInput string) *Error {
	return &Error{Code: CodeInputRequired, Hint: typedInput, Cause: cause}
}

func isPublishedCode(code string) bool {
	return code != internalCode && protovalidate.Validate(&streamv1.RunError{Code: code}) == nil
}
