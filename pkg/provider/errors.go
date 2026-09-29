package provider

import (
	"errors"

	connect "connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/refusal"
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
	var asked QuestionRefusal
	if errors.As(err, &asked) {
		return questionError(err, asked)
	}
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		var already *connect.Error
		if errors.As(err, &already) {
			return err
		}
		return connect.NewError(connect.CodeInternal, err)
	}
	code, ok := refusalCodes[refused.Code]
	if !ok {
		code = connect.CodeInternal
	}
	rpcErr := connect.NewError(code, errors.New(refused.Message))
	if detail, err := connect.NewErrorDetail(&contractv1.Refusal{Code: protoRefusalCodes[refused.Code]}); err == nil {
		rpcErr.AddDetail(detail)
	}
	return rpcErr
}

func questionError(err error, asked QuestionRefusal) error {
	var already *connect.Error
	if errors.As(err, &already) {
		return err
	}
	rpcErr := connect.NewError(connect.CodePermissionDenied, asked)
	if detail, err := connect.NewErrorDetail(&contractv1.Refusal{Code: contractv1.RefusalCode_REFUSAL_CODE_DENIED}); err == nil {
		rpcErr.AddDetail(detail)
	}
	return rpcErr
}

var protoRefusalCodes = map[refusal.Code]contractv1.RefusalCode{
	refusal.CodeInvalid:  contractv1.RefusalCode_REFUSAL_CODE_INVALID,
	refusal.CodeNotReady: contractv1.RefusalCode_REFUSAL_CODE_NOT_READY,
	refusal.CodeDenied:   contractv1.RefusalCode_REFUSAL_CODE_DENIED,
	refusal.CodeBusy:     contractv1.RefusalCode_REFUSAL_CODE_BUSY,

	refusal.CodeUnknownOption: contractv1.RefusalCode_REFUSAL_CODE_UNKNOWN_OPTION,
}

func RefusedCode(err error) (refusal.Code, bool) {
	var refused refusal.Refusal
	if errors.As(err, &refused) {
		return refused.Code, true
	}
	var rpcErr *connect.Error
	if !errors.As(err, &rpcErr) {
		return "", false
	}
	for _, detail := range rpcErr.Details() {
		value, err := detail.Value()
		if err != nil {
			continue
		}
		refused, ok := value.(*contractv1.Refusal)
		if !ok {
			continue
		}
		for code, encoded := range protoRefusalCodes {
			if encoded == refused.GetCode() {
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
