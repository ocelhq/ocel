package provider

import (
	"errors"

	connect "connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

var ErrUnscopedGrant = errors.New("unscoped grant")

var refusalCodes = map[refusal.Code]connect.Code{
	refusal.CodeInvalid:  connect.CodeInvalidArgument,
	refusal.CodeNotReady: connect.CodeFailedPrecondition,
	refusal.CodeDenied:   connect.CodePermissionDenied,
	refusal.CodeBusy:     connect.CodeAborted,

	refusal.CodeUnknownOption: connect.CodeInvalidArgument,
}

func RefusalError(err error) error {
	if err == nil {
		return nil
	}
	var trust HostTrustRefusal
	if errors.As(err, &trust) {
		return hostTrustError(trust)
	}
	var refusal refusal.Refusal
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

var wireRefusalCodes = map[refusal.Code]contractv1.RefusalCode{
	refusal.CodeInvalid:  contractv1.RefusalCode_REFUSAL_CODE_INVALID,
	refusal.CodeNotReady: contractv1.RefusalCode_REFUSAL_CODE_NOT_READY,
	refusal.CodeDenied:   contractv1.RefusalCode_REFUSAL_CODE_DENIED,
	refusal.CodeBusy:     contractv1.RefusalCode_REFUSAL_CODE_BUSY,

	refusal.CodeUnknownOption: contractv1.RefusalCode_REFUSAL_CODE_UNKNOWN_OPTION,
}

func RefusedCode(err error) (refusal.Code, bool) {
	var refusal refusal.Refusal
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

type resumable struct{ cause error }

func (p resumable) Error() string { return p.cause.Error() }

func (p resumable) Unwrap() error { return p.cause }

func Resumable(err error) error {
	if err == nil {
		return nil
	}
	return resumable{cause: err}
}

func ResumableMessage(err error) (string, bool) {
	var r resumable
	if errors.As(err, &r) {
		return r.Error(), true
	}
	return "", false
}
