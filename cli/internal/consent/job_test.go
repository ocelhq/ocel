package consent_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/jobinput"
)

func answerFromTheJob(t *testing.T, term *terminal, stdin *io.PipeWriter, confirmed bool) {
	t.Helper()
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			for _, ev := range term.received() {
				if input := ev.GetWaiting().GetInput(); input != nil {
					_, _ = fmt.Fprintf(stdin, "{\"inputId\":%q,\"confirmed\":%t}\n", input.GetId(), confirmed)
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
}

func jobPolicy(term *terminal) (consent.Policy, *io.PipeWriter) {
	stdin, write := io.Pipe()
	return consent.Policy{Command: "ocel deploy", Job: jobinput.New(stdin), Out: term}, write
}

func TestInsideAJobAConfirmationIsPutToOcelCloudRatherThanAssumed(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		t.Run(fmt.Sprint(confirmed), func(t *testing.T) {
			term := &terminal{}
			policy, stdin := jobPolicy(term)
			answerFromTheJob(t, term, stdin, confirmed)

			guard := newProjectGuard
			guard.Finding = "This backend already has: web"
			span := spanOn(t, term)
			granted, err := policy.Confirm(context.Background(), span, guard)
			if err != nil || granted != confirmed {
				t.Fatalf("Confirm() = %t, %v, want Ocel Cloud's answer %t", granted, err, confirmed)
			}
			var asked bool
			for _, ev := range term.received() {
				if c := ev.GetWaiting().GetInput().GetConfirmation(); c != nil {
					asked = c.GetQuestion() == guard.Question && c.GetFinding() == guard.Finding
				}
			}
			if !asked {
				t.Errorf("stream = %q, want a waiting event asking the guard's question with its finding", shape(term.received()))
			}
			if assumed := summaryOf(t, term, span).GetAssumed(); len(assumed) != 0 {
				t.Errorf("assumed = %v, want nothing assumed: somebody was asked", assumed)
			}
			if !confirmed && !strings.Contains(strings.Join(shape(term.received()), "\n"), "Not confirmed; nothing changed") {
				t.Errorf("stream = %q, want a declined confirmation to say this run changes nothing", shape(term.received()))
			}
		})
	}
}

func TestInsideAJobAQuestionIsNotRefusedForWantOfATerminal(t *testing.T) {
	policy, _ := jobPolicy(&terminal{})
	if err := policy.RefuseQuestion("Remove it?", "before it tears the preview down"); err != nil {
		t.Errorf("RefuseQuestion() = %v, want nil: Ocel Cloud can answer it", err)
	}
}
