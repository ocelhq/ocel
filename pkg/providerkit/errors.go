package providerkit

import (
	"errors"

	connect "connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
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
	var trust HostTrustRefusal
	if errors.As(err, &trust) {
		return hostTrustError(trust)
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
	wire := connect.NewError(code, errors.New(refusal.Message))
	if detail, err := connect.NewErrorDetail(&contractv1.Refusal{Code: wireRefusalCodes[refusal.Code]}); err == nil {
		wire.AddDetail(detail)
	}
	return wire
}

var wireRefusalCodes = map[Code]contractv1.RefusalCode{
	CodeInvalid:  contractv1.RefusalCode_REFUSAL_CODE_INVALID,
	CodeNotReady: contractv1.RefusalCode_REFUSAL_CODE_NOT_READY,
	CodeDenied:   contractv1.RefusalCode_REFUSAL_CODE_DENIED,
	CodeBusy:     contractv1.RefusalCode_REFUSAL_CODE_BUSY,
}

func RefusedCode(err error) (Code, bool) {
	var refusal Refusal
	if errors.As(err, &refusal) {
		return refusal.Code, true
	}
	var wire *connect.Error
	if !errors.As(err, &wire) {
		return "", false
	}
	for _, detail := range wire.Details() {
		value, err := detail.Value()
		if err != nil {
			continue
		}
		carried, ok := value.(*contractv1.Refusal)
		if !ok {
			continue
		}
		for code, encoded := range wireRefusalCodes {
			if encoded == carried.GetCode() {
				return code, true
			}
		}
	}
	return "", false
}
