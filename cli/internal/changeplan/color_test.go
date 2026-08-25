package changeplan

import (
	"bytes"
	"strings"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestRenderPaintsTheSigilAndDimsTheMetadata(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	newPrinter(&out, true).Render("Proposed changes to the production bootstrap", &contractv1.ChangePlan{
		Groups: []*contractv1.ChangeGroup{
			{
				Kind:    "stack",
				Name:    "ocel-production-queues",
				Feature: "queues",
				Action:  contractv1.Change_ACTION_CREATE,
				Changes: []*contractv1.Change{
					{Kind: "AWS::SQS::Queue", Name: "OcelQueue", Action: contractv1.Change_ACTION_CREATE},
				},
			},
			{
				Kind:    "stack",
				Name:    "ocel-production-isr",
				Feature: "isr",
				Action:  contractv1.Change_ACTION_DELETE,
				Reason:  "web, api were deployed against it",
			},
		},
	})

	got := out.String()
	for _, want := range []string{
		"\x1b[32m+\x1b[0m \x1b[1mocel-production-queues\x1b[22m  \x1b[2m[queues]\x1b[22m",
		"\x1b[32m+\x1b[0m OcelQueue  \x1b[2mAWS::SQS::Queue\x1b[22m",
		"\x1b[31m–\x1b[0m \x1b[1mocel-production-isr\x1b[22m  \x1b[2m[isr]\x1b[22m\x1b[2m  — web, api were deployed against it\x1b[22m",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "\x1b[32m2 to create") {
		t.Errorf("Render() = %q, want the tally left in the default colour", got)
	}
}

func TestRenderOnANonTerminalStaysPlain(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	NewPrinter(&out).Render("Proposed changes to the production bootstrap", &contractv1.ChangePlan{
		Groups: []*contractv1.ChangeGroup{
			{Kind: "stack", Name: "ocel-production-core", Action: contractv1.Change_ACTION_CREATE},
		},
	})
	if strings.Contains(out.String(), "\x1b[") {
		t.Errorf("Render() = %q, want no escape codes when the writer is not a terminal", out.String())
	}
}
