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

func askingPolicy(term *terminal, answer string) consent.Policy {
	return consent.Policy{Command: "ocel test", Interactive: true, In: strings.NewReader(answer), Out: term}
}

func TestAnInteractionHoldsTheStreamWhileItAsks(t *testing.T) {
	term := &terminal{}
	span := spanOn(t, term)

	granted, err := askingPolicy(term, "y\n").Confirm(context.Background(), span, `Tear down the named preview "staging"?`)
	if err != nil || !granted {
		t.Fatalf("Confirm() = %v, %v, want the answered yes to grant it", granted, err)
	}
	if !strings.Contains(term.whileHeld, "Tear down the named preview") {
		t.Errorf("written while the stream was held = %q, all written = %q, want the question put while the sinks yield the terminal", term.whileHeld, term.written())
	}
}

func TestAPolicyThatConfirmsNoPlanRefusesNothingOfItsOwn(t *testing.T) {
	if err := (consent.Policy{Command: "ocel test"}).Refuse(); err != nil {
		t.Errorf("Refuse() = %v, want a convergent command to proceed with no terminal and no --yes", err)
	}
}

func confirmingPlan() consent.Policy {
	return consent.Policy{Command: "ocel test", ConfirmsPlan: true}
}

func TestAPolicyThatConfirmsThePlanRefusesOffATerminalAndSaysHowToProceed(t *testing.T) {
	err := confirmingPlan().Refuse()
	if err == nil {
		t.Fatal("Refuse() = nil, want a refusal with no terminal to consent on")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("Refuse() = %q, want the refusal to name --yes", err)
	}
}

func TestAPolicyThatConfirmsThePlanTakesYesInPlaceOfATerminal(t *testing.T) {
	g := confirmingPlan()
	g.Yes = true
	if err := g.Refuse(); err != nil {
		t.Errorf("Refuse() = %v, want --yes to answer in place of the terminal", err)
	}
}

func TestADryRunNeedsNoConsentAtAll(t *testing.T) {
	g := confirmingPlan()
	g.DryRun = true
	if err := g.Refuse(); err != nil {
		t.Errorf("Refuse() = %v, want --dry to reach the plan with nothing to consent to", err)
	}
}

func TestAPolicyThatConfirmsThePlanTakesATerminalInPlaceOfYes(t *testing.T) {
	g := confirmingPlan()
	g.Interactive = true
	if err := g.Refuse(); err != nil {
		t.Errorf("Refuse() = %v, want a terminal to be consent enough to reach the plan", err)
	}
}

func TestThePlanRefusalReadsTrueForACommandThatCreates(t *testing.T) {
	g := confirmingPlan()
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
	g := confirmingPlan()
	g.Command = "ocel destroy production"
	g.UnattendedRemedy = "set OCEL_DESTROY_BYPASS_CONFIRMATION to the project name"
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

func TestAConfirmationIsGrantedInAdvanceByYes(t *testing.T) {
	term := &terminal{}
	g := askingPolicy(term, "n\n")
	g.Yes = true

	granted, err := g.Confirm(context.Background(), spanOn(t, term), teardown)
	if err != nil || !granted {
		t.Errorf("Confirm() = %v, %v, want --yes to answer it in advance", granted, err)
	}
	if strings.Contains(term.written(), "Tear down") {
		t.Errorf("written = %q, want --yes to leave the confirmation unasked", term.written())
	}
}

func TestADryRunAsksNoConfirmation(t *testing.T) {
	term := &terminal{}
	g := askingPolicy(term, "n\n")
	g.DryRun = true

	granted, err := g.Confirm(context.Background(), spanOn(t, term), teardown)
	if err != nil || !granted {
		t.Errorf("Confirm() = %v, %v, want a run that changes nothing to need no confirmation", granted, err)
	}
	if strings.Contains(term.written(), "Tear down") {
		t.Errorf("written = %q, want --dry to leave the confirmation unasked: there is nothing to confirm", term.written())
	}
}

func TestAConfirmationSkipsWhenThereIsNoTerminalToAskOn(t *testing.T) {
	term := &terminal{}
	g := askingPolicy(term, "")
	g.Interactive = false

	granted, err := g.Confirm(context.Background(), spanOn(t, term), teardown)
	if err != nil || !granted {
		t.Errorf("Confirm() = %v, %v, want a confirmation to skip and proceed off a terminal", granted, err)
	}
	if strings.Contains(term.written(), "Tear down") {
		t.Errorf("written = %q, want no question asked where nothing can answer it", term.written())
	}
}

func TestAConfirmationIsAskedOnATerminalAndANoStopsTheCommand(t *testing.T) {
	term := &terminal{}

	granted, err := askingPolicy(term, "n\n").Confirm(context.Background(), spanOn(t, term), teardown)
	if err != nil || granted {
		t.Errorf("Confirm() = %v, %v, want the answered no to withhold it", granted, err)
	}
	if !strings.Contains(term.written(), "Tear down the named preview") {
		t.Errorf("written = %q, want the confirmation's question put to the terminal", term.written())
	}
}

func TestADeclinedConfirmationSaysSoOnTheStreamOnceTheStreamIsResumed(t *testing.T) {
	term := &terminal{}

	if _, err := askingPolicy(term, "n\n").Confirm(context.Background(), spanOn(t, term), teardown); err != nil {
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
	g := askingPolicy(term, "")
	g.ConfirmsPlan, g.Yes, g.Interactive = true, true, false

	granted, err := g.ConfirmPlanByName(context.Background(), spanOn(t, term), nil, "project name", "acme")
	if err != nil || !granted {
		t.Errorf("ConfirmPlanByName() = %v, %v, want --yes to grant the plan it confirms", granted, err)
	}
	if strings.Contains(term.written(), "project name") {
		t.Errorf("written = %q, want --yes to leave the ceremony unasked", term.written())
	}
}

func TestPlanConsentOffATerminalIsRefusedWithTheRemedy(t *testing.T) {
	term := &terminal{}
	g := confirmingPlan()
	g.Out = term

	granted, err := g.ConfirmPlanByName(context.Background(), spanOn(t, term), nil, "project name", "acme")
	if granted || err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("ConfirmPlanByName() = %v, %v, want a refusal naming --yes where nothing can answer", granted, err)
	}
}

func TestPlanConsentOnATerminalIsTheTypedNameCeremony(t *testing.T) {
	term := &terminal{}

	granted, err := askingPolicy(term, "acme\n").ConfirmPlanByName(context.Background(), spanOn(t, term), nil, "project name", "acme")
	if err != nil || !granted {
		t.Errorf("ConfirmPlanByName() = %v, %v, want the typed name to grant it", granted, err)
	}
	if !strings.Contains(term.written(), "project name") {
		t.Errorf("written = %q, want the ceremony to name what has to be typed", term.written())
	}
}

func TestPlanConsentIsWithheldWhenTheNameIsNotTypedBack(t *testing.T) {
	term := &terminal{}

	granted, err := askingPolicy(term, "something else\n").ConfirmPlanByName(context.Background(), spanOn(t, term), nil, "project name", "acme")
	if err != nil || granted {
		t.Errorf("ConfirmPlanByName() = %v, %v, want a mistyped name to withhold it", granted, err)
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
			g := consent.NewPolicy("ocel test", tc.yes, tc.interactive, &bytes.Buffer{}, strings.NewReader(""))
			if got := g.IsAsking(); got != tc.want {
				t.Errorf("IsAsking() = %v, want %v", got, tc.want)
			}
		})
	}
}
