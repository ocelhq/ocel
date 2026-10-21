package providerprocess

import (
	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/wireerror"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func providerRefusal(err *connect.Error) *clierror.Error {
	return codedRefusal(err.Code(), wireerror.Error{Message: err.Message(), Wire: err})
}

func failedOperation(result *progressv1.OperationResult) error {
	failed := &OperationFailedError{Message: result.GetError()}
	var code connect.Code
	if code.UnmarshalText([]byte(result.GetConnectCode())) != nil {
		return failed
	}
	if coded := codedRefusal(code, failed); coded != nil {
		return coded
	}
	return failed
}

func codedRefusal(code connect.Code, cause error) *clierror.Error {
	switch code {
	case connect.CodeInvalidArgument, connect.CodeOutOfRange, connect.CodeAlreadyExists:
		return &clierror.Error{Code: clierror.CodeProviderInvalidRequest, Cause: cause}
	case connect.CodeNotFound:
		return &clierror.Error{Code: clierror.CodeProviderNotFound, Cause: cause}
	case connect.CodeFailedPrecondition:
		return &clierror.Error{Code: clierror.CodeProviderNotReady, Cause: cause}
	case connect.CodePermissionDenied:
		return &clierror.Error{Code: clierror.CodeProviderDenied, Cause: cause}
	case connect.CodeAborted, connect.CodeResourceExhausted:
		return &clierror.Error{Code: clierror.CodeProviderBusy, Retryable: true, Cause: cause}
	case connect.CodeUnimplemented:
		return &clierror.Error{Code: clierror.CodeProviderUnsupported, Cause: cause}
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded:
		return &clierror.Error{Code: clierror.CodeProviderUnavailable, Retryable: true, Cause: cause}
	}
	return nil
}
