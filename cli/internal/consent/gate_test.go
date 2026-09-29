package consent_test

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/run"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type terminal struct {
	mu        sync.Mutex
	out       bytes.Buffer
	events    []*streamv1.RunEvent
	whileHeld string
	heldAt    int
}

func (t *terminal) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.out.Write(p)
}

func (t *terminal) Receive(ev *streamv1.RunEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, ev)
	switch {
	case ev.GetWaiting() != nil:
		t.heldAt = t.out.Len()
	case ev.GetResumed() != nil:
		t.whileHeld = t.out.String()[t.heldAt:]
	}
}

func (t *terminal) Close() error { return nil }

func (t *terminal) written() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.out.String()
}

func (t *terminal) received() []*streamv1.RunEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*streamv1.RunEvent(nil), t.events...)
}

func shape(evs []*streamv1.RunEvent) []string {
	var out []string
	for _, ev := range evs {
		switch {
		case ev.GetWaiting() != nil:
			out = append(out, "waiting")
		case ev.GetResumed() != nil:
			out = append(out, "resumed")
		case ev.GetBody() == nil:
			out = append(out, ev.GetMessage())
		default:
			out = append(out, "event")
		}
	}
	return out
}

func spanOn(t *testing.T, term *terminal) *run.Span {
	t.Helper()
	bus := run.NewBus(time.Now)
	bus.Attach(term)
	_, run, err := bus.Begin(context.Background(), "ocel test", "")
	if err != nil {
		t.Fatal(err)
	}
	return run.Phase(progressv1.Phase_PHASE_PLAN)
}

func askingGate(term *terminal, answer string) consent.Gate {
	return consent.Gate{Command: "ocel test", Interactive: true, In: strings.NewReader(answer), Out: term}
}

func TestAnInteractionHoldsTheStreamWhileItAsks(t *testing.T) {
	term := &terminal{}
	span := spanOn(t, term)

	granted, err := askingGate(term, "y\n").Guard(context.Background(), span, `Tear down the named preview "staging"?`)
	if err != nil || !granted {
		t.Fatalf("Guard() = %v, %v, want the answered yes to grant it", granted, err)
	}
	if !strings.Contains(term.whileHeld, "Tear down the named preview") {
		t.Errorf("written while the stream was held = %q, all written = %q, want the question put while the sinks yield the terminal", term.whileHeld, term.written())
	}
}

func TestTheConvergentClassGatesNothingOfItsOwn(t *testing.T) {
	if err := (consent.Gate{Command: "ocel test", Class: consent.Convergent}).Refuse(); err != nil {
		t.Errorf("Refuse() = %v, want a convergent command to proceed with no terminal and no --yes", err)
	}
}

func planFirst() consent.Gate {
	return consent.Gate{Command: "ocel test", Class: consent.PlanFirst, Unattended: "pass --yes"}
}

func TestThePlanFirstClassRefusesOffATerminalAndSaysHowToProceed(t *testing.T) {
	err := planFirst().Refuse()
	if err == nil {
		t.Fatal("Refuse() = nil, want a refusal with no terminal to consent on")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("Refuse() = %q, want the refusal to name --yes", err)
	}
}

func TestThePlanFirstClassTakesYesInPlaceOfATerminal(t *testing.T) {
	g := planFirst()
	g.Yes = true
	if err := g.Refuse(); err != nil {
		t.Errorf("Refuse() = %v, want --yes to answer in place of the terminal", err)
	}
}

func TestADryRunNeedsNoConsentAtAll(t *testing.T) {
	g := planFirst()
	g.Dry = true
	if err := g.Refuse(); err != nil {
		t.Errorf("Refuse() = %v, want --dry to reach the plan with nothing to consent to", err)
	}
}

func TestThePlanFirstClassTakesATerminalInPlaceOfYes(t *testing.T) {
	g := planFirst()
	g.Interactive = true
	if err := g.Refuse(); err != nil {
		t.Errorf("Refuse() = %v, want a terminal to be consent enough to reach the plan", err)
	}
}

func TestThePlanFirstRefusalReadsTrueForACommandThatCreates(t *testing.T) {
	g := planFirst()
	g.Command = "ocel bootstrap production"
	err := g.Refuse()
	if err == nil {
		t.Fatal("Refuse() = nil, want a refusal with no terminal to consent on")
	}
	if strings.Contains(err.Error(), "remove") {
		t.Errorf("Refuse() = %q, want a refusal that describes the plan, not one that assumes it destroys", err)
	}
}

func TestTheRefusalNamesTheCommandAndTheRemedyThatCommandOffers(t *testing.T) {
	g := planFirst()
	g.Command = "ocel destroy production"
	g.Unattended = "set OCEL_DESTROY_BYPASS_CONFIRMATION to the project name"
	err := g.Refuse()
	if err == nil {
		t.Fatal("Refuse() = nil, want a refusal with no terminal to consent on")
	}
	if !strings.Contains(err.Error(), "ocel destroy production") {
		t.Errorf("Refuse() = %q, want the refusal to name the command that was refused", err)
	}
	if !strings.Contains(err.Error(), "OCEL_DESTROY_BYPASS_CONFIRMATION") {
		t.Errorf("Refuse() = %q, want the refusal to offer the remedy this command actually has, not --yes", err)
	}
}

const teardown = `Tear down the named preview "staging"?`

func TestAConvergentGuardIsGrantedInAdvanceByYes(t *testing.T) {
	term := &terminal{}
	g := askingGate(term, "n\n")
	g.Yes = true

	granted, err := g.Guard(context.Background(), spanOn(t, term), teardown)
	if err != nil || !granted {
		t.Errorf("Guard() = %v, %v, want --yes to answer it in advance", granted, err)
	}
	if strings.Contains(term.written(), "Tear down") {
		t.Errorf("written = %q, want --yes to leave the guard unasked", term.written())
	}
}

func TestAConvergentDryRunAsksNothing(t *testing.T) {
	term := &terminal{}
	g := askingGate(term, "n\n")
	g.Dry = true

	granted, err := g.Guard(context.Background(), spanOn(t, term), teardown)
	if err != nil || !granted {
		t.Errorf("Guard() = %v, %v, want a run that changes nothing to need no guard", granted, err)
	}
	if strings.Contains(term.written(), "Tear down") {
		t.Errorf("written = %q, want --dry to leave the guard unasked: there is nothing to guard against", term.written())
	}
}

func TestAConvergentGuardSkipsWhenThereIsNoTerminalToAskOn(t *testing.T) {
	term := &terminal{}
	g := askingGate(term, "")
	g.Interactive = false

	granted, err := g.Guard(context.Background(), spanOn(t, term), teardown)
	if err != nil || !granted {
		t.Errorf("Guard() = %v, %v, want a guard to skip and proceed off a terminal", granted, err)
	}
	if strings.Contains(term.written(), "Tear down") {
		t.Errorf("written = %q, want no question asked where nothing can answer it", term.written())
	}
}

func TestAConvergentGuardIsAskedOnATerminalAndANoStopsTheCommand(t *testing.T) {
	term := &terminal{}

	granted, err := askingGate(term, "n\n").Guard(context.Background(), spanOn(t, term), teardown)
	if err != nil || granted {
		t.Errorf("Guard() = %v, %v, want the answered no to withhold it", granted, err)
	}
	if !strings.Contains(term.written(), "Tear down the named preview") {
		t.Errorf("written = %q, want the guard's question put to the terminal", term.written())
	}
}

func TestADeclinedGuardSaysSoOnTheStreamOnceTheStreamIsResumed(t *testing.T) {
	term := &terminal{}

	if _, err := askingGate(term, "n\n").Guard(context.Background(), spanOn(t, term), teardown); err != nil {
		t.Fatal(err)
	}
	got := term.received()
	last := got[len(got)-1]
	if last.GetMessage() != "Not confirmed, so this run changes nothing" || last.GetBody() != nil || got[len(got)-2].GetResumed() == nil {
		t.Errorf("stream = %q, want the refusal sent as a message event after the resume, not written past the stream", shape(got))
	}
}

func TestPlanConsentIsGrantedInAdvanceByYesWithoutAskingAgain(t *testing.T) {
	term := &terminal{}
	g := askingGate(term, "")
	g.Class, g.Yes, g.Interactive = consent.PlanFirst, true, false

	granted, err := g.ConsentByName(context.Background(), spanOn(t, term), nil, "project name", "acme")
	if err != nil || !granted {
		t.Errorf("ConsentByName() = %v, %v, want --yes to grant the gate this class raises", granted, err)
	}
	if strings.Contains(term.written(), "project name") {
		t.Errorf("written = %q, want --yes to leave the ceremony unasked", term.written())
	}
}

func TestPlanConsentOffATerminalIsRefusedWithTheRemedy(t *testing.T) {
	term := &terminal{}
	g := planFirst()
	g.Out = term

	granted, err := g.ConsentByName(context.Background(), spanOn(t, term), nil, "project name", "acme")
	if granted || err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("ConsentByName() = %v, %v, want a refusal naming --yes where nothing can answer", granted, err)
	}
}

func TestPlanConsentOnATerminalIsTheTypedNameCeremony(t *testing.T) {
	term := &terminal{}

	granted, err := askingGate(term, "acme\n").ConsentByName(context.Background(), spanOn(t, term), nil, "project name", "acme")
	if err != nil || !granted {
		t.Errorf("ConsentByName() = %v, %v, want the typed name to grant it", granted, err)
	}
	if !strings.Contains(term.written(), "project name") {
		t.Errorf("written = %q, want the ceremony to name what has to be typed", term.written())
	}
}

func TestPlanConsentIsWithheldWhenTheNameIsNotTypedBack(t *testing.T) {
	term := &terminal{}

	granted, err := askingGate(term, "something else\n").ConsentByName(context.Background(), spanOn(t, term), nil, "project name", "acme")
	if err != nil || granted {
		t.Errorf("ConsentByName() = %v, %v, want a mistyped name to withhold it", granted, err)
	}
	if got := term.received(); got[len(got)-1].GetMessage() != "Not confirmed, so this run changes nothing" {
		t.Errorf("stream = %q, want a withheld consent to say the command stopped", shape(got))
	}
}

func TestYesTakesTheCommandOutOfTheAskingBusinessAltogether(t *testing.T) {
	for _, tc := range []struct {
		name        string
		yes         bool
		interactive bool
		want        bool
	}{
		{"a terminal and no --yes", false, true, true},
		{"a terminal with --yes", true, true, false},
		{"no terminal", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := consent.Gate{Yes: tc.yes, Interactive: tc.interactive}
			if got := g.Asking(); got != tc.want {
				t.Errorf("Asking() = %v, want %v", got, tc.want)
			}
		})
	}
}
