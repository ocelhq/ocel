package runui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

func TestAPlainCheckDrawsWhoItActsAsWhatItSaysAndWarnsButNotItsUnitsOrDebugLines(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	identity := &streamv1.IdentityEvent{Project: "acme", Origin: &streamv1.Party{Vendor: "aws", Account: "123456789012"}}
	err := PlainCheck(Presentation{}, &out, &providerclient.Runner{}, func(check *events.Scope, _ *providerclient.Provider) error {
		check.Unit("aws", "Checking credentials").End(nil)
		check.Identity(identity)
		check.Say("created the queue")
		check.Debug("+  aws:sqs:Queue isr creating (0s)")
		check.Warn("the old queue outlived its release")
		return nil
	})
	if err != nil {
		t.Fatalf("PlainCheck() = %v", err)
	}

	want := strings.Join(IdentityBlock(Presentation{}, identity), "\n") + "\ncreated the queue\n⚠ the old queue outlived its release\n"
	if out.String() != want {
		t.Errorf("drew %q, want %q", out.String(), want)
	}
}
