package gcp

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestAHostnameIsSettledOnWhatItAnswersAndNotOnWhatWasJustBound(t *testing.T) {
	t.Parallel()

	var held any = &Provider{}
	if _, probes := held.(providerkit.Prober); !probes {
		t.Error("the gcp provider supplies no probe, so the kit settles a hostname from the record it just wrote and a deploy prints a url nothing answers yet")
	}
	if _, says := held.(providerkit.Diagnoser); !says {
		t.Error("the gcp provider's probe says nothing of what stopped it, so a settle that gives up has no cause to name")
	}
}
