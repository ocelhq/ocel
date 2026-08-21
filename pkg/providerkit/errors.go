package providerkit

import (
	"errors"

	connect "connectrpc.com/connect"
)

var refusalCodes = map[Code]connect.Code{
	CodeInvalid:  connect.CodeInvalidArgument,
	CodeNotReady: connect.CodeFailedPrecondition,
	CodeDenied:   connect.CodePermissionDenied,
	CodeBusy:     connect.CodeAborted,
}

func refusalError(err error) error {
	if err == nil {
		return nil
	}
	var refusal Refusal
	if !errors.As(err, &refusal) {
		return connect.NewError(connect.CodeInternal, err)
	}
	code, ok := refusalCodes[refusal.Code]
	if !ok {
		code = connect.CodeInternal
	}
	return connect.NewError(code, errors.New(refusal.Message))
}
