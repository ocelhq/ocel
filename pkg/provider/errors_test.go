package provider

import (
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestAnUnsupportedRefusalCrossesTheWireUnderItsOwnCode(t *testing.T) {
	t.Parallel()

	err := RefusalError(refusal.Refuse(refusal.CodeUnsupported, "tasks are unsupported here"))
	if got := connect.CodeOf(err); got != connect.CodeUnimplemented {
		t.Errorf("RefusalError(unsupported) code = %v, want %v", got, connect.CodeUnimplemented)
	}
	if code, named := RefusedCode(err); !named || code != refusal.CodeUnsupported {
		t.Errorf("RefusedCode() = %q, %v, want %q read back from the wire", code, named, refusal.CodeUnsupported)
	}
}

func TestANeedsRunnerRefusalCrossesTheWireAsAFailedPreconditionTheClientCanName(t *testing.T) {
	t.Parallel()

	err := RefusalError(refusal.Refuse(refusal.CodeNeedsRunner, "this endpoint runs no provisioning engine"))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Errorf("RefusalError(needs-runner) code = %v, want %v", got, connect.CodeFailedPrecondition)
	}
	if code, named := RefusedCode(err); !named || code != refusal.CodeNeedsRunner {
		t.Errorf("RefusedCode() = %q, %v, want %q read back from the wire", code, named, refusal.CodeNeedsRunner)
	}
}
