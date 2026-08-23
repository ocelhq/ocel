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

func RefusalError(err error) error {
	if err == nil {
		return nil
	}
	var refusal Refusal
	if !errors.As(err, &refusal) {
		var already *connect.Error
		if errors.As(err, &already) {
			return err
		}
		return connect.NewError(connect.CodeInternal, err)
	}
	code, ok := refusalCodes[refusal.Code]
	if !ok {
		code = connect.CodeInternal
	}
	return connect.NewError(code, errors.New(refusal.Message))
}
