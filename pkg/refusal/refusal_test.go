package refusal_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestAnOptionRefusalIsKnownThroughTheErrorsWrappingItAndKeepsItsCode(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("operation error STS: GetCallerIdentity: %w",
		refusal.RefuseOption(refusal.CodeInvalid, "option %q names %q", "region", "eu-wset-1"))

	if !refusal.IsOption(wrapped) {
		t.Errorf("IsOption(%v) = false, want an option refusal known however it is wrapped", wrapped)
	}
	var refused refusal.Refusal
	if !errors.As(wrapped, &refused) || refused.Code != refusal.CodeInvalid || refused.Message != `option "region" names "eu-wset-1"` {
		t.Errorf("the refusal within %v = %+v, want its code and message kept", wrapped, refused)
	}
}

func TestARefusalOfNoOptionIsNotAnOptionRefusal(t *testing.T) {
	t.Parallel()

	if err := refusal.Refuse(refusal.CodeDenied, "no credentials"); refusal.IsOption(err) {
		t.Errorf("IsOption(%v) = true, want false for a refusal no option caused", err)
	}
}
