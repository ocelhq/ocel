package providerprocess

import (
	"errors"
	"strings"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/clierror"
)

type refusedCall struct {
	message string
	cause   error
}

func (r refusedCall) Error() string { return r.message }

func (r refusedCall) Unwrap() error { return r.cause }

func refusalIn(err error) error {
	if err == nil || errors.As(err, new(*clierror.Error)) {
		return err
	}
	code, message, refused := refusalOf(err)
	if !refused {
		return err
	}
	if coded := codedRefusal(code, refusedCall{message: message, cause: err}); coded != nil {
		return coded
	}
	return err
}

func codedRefusal(code connect.Code, refused refusedCall) *clierror.Error {
	switch code {
	case connect.CodeInvalidArgument, connect.CodeOutOfRange, connect.CodeAlreadyExists:
		return &clierror.Error{Code: clierror.CodeProviderInvalidRequest, Cause: refused}
	case connect.CodeNotFound:
		return &clierror.Error{Code: clierror.CodeProviderNotFound, Cause: refused}
	case connect.CodeFailedPrecondition:
		return &clierror.Error{Code: clierror.CodeProviderNotReady, Cause: refused}
	case connect.CodePermissionDenied:
		return &clierror.Error{Code: clierror.CodeProviderDenied, Cause: refused}
	case connect.CodeAborted, connect.CodeResourceExhausted:
		return &clierror.Error{Code: clierror.CodeProviderBusy, Retryable: true, Cause: refused}
	case connect.CodeUnimplemented:
		return &clierror.Error{Code: clierror.CodeProviderUnsupported, Cause: refused}
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded:
		return &clierror.Error{Code: clierror.CodeProviderUnavailable, Retryable: true, Cause: refused}
	}
	return nil
}

func refusalOf(err error) (connect.Code, string, bool) {
	var failed *OperationFailedError
	if errors.As(err, &failed) {
		var code connect.Code
		if failed.ConnectCode == "" || code.UnmarshalText([]byte(failed.ConnectCode)) != nil {
			return 0, "", false
		}
		return code, strings.Replace(err.Error(), failed.Message, strings.TrimPrefix(failed.Message, failed.ConnectCode+": "), 1), true
	}
	var rpcErr *connect.Error
	if !errors.As(err, &rpcErr) {
		return 0, "", false
	}
	return rpcErr.Code(), strings.Replace(err.Error(), rpcErr.Error(), rpcErr.Message(), 1), true
}
