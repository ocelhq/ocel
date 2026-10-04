package consent_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/clierror"
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
		case ev.GetOperation().GetBody() == nil && ev.GetCli() == nil:
			out = append(out, ev.GetOperation().GetMessage())
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

	granted, err := askingPolicy(term, "y\n").Confirm(context.Background(), span, newProjectGuard)
	if err != nil || !granted {
		t.Fatalf("Confirm() = %v, %v, want the answered yes to grant it", granted, err)
	}
	if !strings.Contains(term.whileHeld, newProjectGuard.Question) {
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

var newProjectGuard = consent.Guard{
	ID:       consent.GuardNewProject,
	Question: `Create the new project "shop"?`,
	Action:   `creating new project "shop"`,
}

const newProjectAssumption = `creating new project "shop" without confirmation (no terminal)`

func summaryOf(t *testing.T, term *terminal, span *run.Span) *streamv1.RunSummary {
	t.Helper()
	var err error
	span.Run().End(&err)
	evs := term.received()
	return evs[len(evs)-1].GetSummary()
}

func TestAConfirmationIsGrantedInAdvanceByYes(t *testing.T) {
	term := &terminal{}
	g := askingPolicy(term, "n\n")
	g.Yes = true

	granted, err := g.Confirm(context.Background(), spanOn(t, term), newProjectGuard)
	if err != nil || !granted {
		t.Errorf("Confirm() = %v, %v, want --yes to answer it in advance", granted, err)
	}
	if strings.Contains(term.written(), newProjectGuard.Question) {
		t.Errorf("written = %q, want --yes to leave the confirmation unasked", term.written())
	}
}

func TestADryRunAsksNoConfirmation(t *testing.T) {
	term := &terminal{}
	g := askingPolicy(term, "n\n")
	g.DryRun = true

	granted, err := g.Confirm(context.Background(), spanOn(t, term), newProjectGuard)
	if err != nil || !granted {
		t.Errorf("Confirm() = %v, %v, want a run that changes nothing to need no confirmation", granted, err)
	}
	if strings.Contains(term.written(), newProjectGuard.Question) {
		t.Errorf("written = %q, want --dry to leave the confirmation unasked: there is nothing to confirm", term.written())
	}
}

func TestAConfirmationSkipsWhenThereIsNoTerminalToAskOn(t *testing.T) {
	term := &terminal{}
	g := askingPolicy(term, "")
	g.Interactive = false

	granted, err := g.Confirm(context.Background(), spanOn(t, term), newProjectGuard)
	if err != nil || !granted {
		t.Errorf("Confirm() = %v, %v, want a confirmation to skip and proceed off a terminal", granted, err)
	}
	if strings.Contains(term.written(), newProjectGuard.Question) {
		t.Errorf("written = %q, want no question asked where nothing can answer it", term.written())
	}
}

func TestAConfirmationIsAskedOnATerminalAndANoStopsTheCommand(t *testing.T) {
	term := &terminal{}

	granted, err := askingPolicy(term, "n\n").Confirm(context.Background(), spanOn(t, term), newProjectGuard)
	if err != nil || granted {
		t.Errorf("Confirm() = %v, %v, want the answered no to withhold it", granted, err)
	}
	if !strings.Contains(term.written(), newProjectGuard.Question) {
		t.Errorf("written = %q, want the confirmation's question put to the terminal", term.written())
	}
}

func TestADeclinedConfirmationSaysSoOnTheStreamOnceTheStreamIsResumed(t *testing.T) {
	term := &terminal{}

	if _, err := askingPolicy(term, "n\n").Confirm(context.Background(), spanOn(t, term), newProjectGuard); err != nil {
		t.Fatal(err)
	}
	got := term.received()
	last := got[len(got)-1]
	if last.GetOperation().GetMessage() != "Not confirmed, so this run changes nothing" || (last.GetOperation().GetBody() != nil || last.GetCli() != nil) || got[len(got)-2].GetResumed() == nil {
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
	if got := term.received(); got[len(got)-1].GetOperation().GetMessage() != "Not confirmed, so this run changes nothing" {
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

const setUp = "Set up production now?"

func TestAnOfferIsPutEvenToADryRun(t *testing.T) {
	term := &terminal{}
	g := askingPolicy(term, "n\n")
	g.DryRun = true

	granted, err := g.Offer(context.Background(), spanOn(t, term), setUp)
	if err != nil || granted {
		t.Errorf("Offer() = %v, %v, want the no a dry run answered to withhold it", granted, err)
	}
	if !strings.Contains(term.written(), setUp) {
		t.Errorf("written = %q, want the offer put to the terminal", term.written())
	}
}

func TestADeclinedOfferLeavesTheCallerToSayWhatHappensNext(t *testing.T) {
	term := &terminal{}

	if _, err := askingPolicy(term, "n\n").Offer(context.Background(), spanOn(t, term), setUp); err != nil {
		t.Fatal(err)
	}
	for _, ev := range term.received() {
		if strings.HasPrefix(ev.GetOperation().GetMessage(), "Not confirmed") {
			t.Errorf("stream = %q, want no line of its own after a declined offer", shape(term.received()))
		}
	}
}

func TestAnOfferIsTakenInAdvanceByYes(t *testing.T) {
	term := &terminal{}
	g := askingPolicy(term, "n\n")
	g.Yes = true

	granted, err := g.Offer(context.Background(), spanOn(t, term), setUp)
	if err != nil || !granted {
		t.Errorf("Offer() = %v, %v, want --yes to take it in advance", granted, err)
	}
	if strings.Contains(term.written(), setUp) {
		t.Errorf("written = %q, want --yes to leave the offer unasked", term.written())
	}
}

func TestASkippedConfirmationWarnsWhatItAssumedAndListsItOnTheSummary(t *testing.T) {
	term := &terminal{}
	g := askingPolicy(term, "")
	g.Interactive = false
	span := spanOn(t, term)

	if granted, err := g.Confirm(context.Background(), span, newProjectGuard); err != nil || !granted {
		t.Fatalf("Confirm() = %v, %v, want the skip to proceed", granted, err)
	}

	var warned bool
	for _, ev := range term.received() {
		if ev.GetOperation().GetLevel() == progressv1.Level_LEVEL_WARN && ev.GetOperation().GetMessage() == newProjectAssumption {
			warned = true
		}
	}
	if !warned {
		t.Errorf("stream = %q, want a WARN naming the guard that was skipped", shape(term.received()))
	}
	assumed := summaryOf(t, term, span).GetAssumed()
	if len(assumed) != 1 || assumed[0].GetId() != "new_project" || assumed[0].GetWarning() != newProjectAssumption {
		t.Errorf("assumed = %v, want the skipped guard listed with its id and warning", assumed)
	}
}

func TestAConfirmationGrantedByYesAssumesNothing(t *testing.T) {
	term := &terminal{}
	g := askingPolicy(term, "")
	g.Interactive = false
	g.Yes = true
	span := spanOn(t, term)

	if _, err := g.Confirm(context.Background(), span, newProjectGuard); err != nil {
		t.Fatal(err)
	}
	if assumed := summaryOf(t, term, span).GetAssumed(); len(assumed) != 0 {
		t.Errorf("assumed = %v, want --yes to be an answer, not an assumption", assumed)
	}
}

func TestADryRunAssumesNothingWhereNoTerminalCouldAnswer(t *testing.T) {
	term := &terminal{}
	g := askingPolicy(term, "")
	g.Interactive = false
	g.DryRun = true
	span := spanOn(t, term)

	if _, err := g.Confirm(context.Background(), span, newProjectGuard); err != nil {
		t.Fatal(err)
	}
	if assumed := summaryOf(t, term, span).GetAssumed(); len(assumed) != 0 {
		t.Errorf("assumed = %v, want a run that changes nothing to assume nothing", assumed)
	}
}

func TestAConfirmationAnsweredYesOnATerminalAssumesNothing(t *testing.T) {
	term := &terminal{}
	span := spanOn(t, term)

	if granted, err := askingPolicy(term, "y\n").Confirm(context.Background(), span, newProjectGuard); err != nil || !granted {
		t.Fatalf("Confirm() = %v, %v, want yes to grant", granted, err)
	}
	if assumed := summaryOf(t, term, span).GetAssumed(); len(assumed) != 0 {
		t.Errorf("assumed = %v, want an answered question to assume nothing", assumed)
	}
}

func TestAConfirmationAnsweredNoOnATerminalAssumesNothing(t *testing.T) {
	term := &terminal{}
	span := spanOn(t, term)

	if granted, err := askingPolicy(term, "n\n").Confirm(context.Background(), span, newProjectGuard); err != nil || granted {
		t.Fatalf("Confirm() = %v, %v, want no to withhold", granted, err)
	}
	if assumed := summaryOf(t, term, span).GetAssumed(); len(assumed) != 0 {
		t.Errorf("assumed = %v, want a declined question to assume nothing", assumed)
	}
}

func TestAPlanRefusedForWantOfATerminalFailsWithConfirmationRequiredHintingYes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remedy string
	}{
		{"the default remedy", ""},
		{"a command's own remedy", "pass --yes, or set OCEL_DESTROY_BYPASS_CONFIRMATION to the project name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := confirmingPlan()
			g.UnattendedRemedy = tc.remedy

			got := clierror.NewRunError(fmt.Errorf("deploy: %w", g.Refuse()))

			if got.GetCode() != clierror.CodeConfirmationRequired || got.GetHint() != "--yes" {
				t.Errorf("run error = %s, want code confirmation_required and hint --yes", protojson.Format(got))
			}
		})
	}
}

func TestAPlanShownToNoTerminalFailsWithConfirmationRequired(t *testing.T) {
	_, err := confirmingPlan().ConfirmPlan(context.Background(), spanOn(t, &terminal{}), mutatingPlan(), "Apply?")

	if got := clierror.NewRunError(err).GetCode(); got != clierror.CodeConfirmationRequired {
		t.Errorf("code = %q, want confirmation_required", got)
	}
}

const teardown = `Tear down the named preview "staging"?`

func TestAQuestionWithNoTerminalToAskOnIsRefusedWithConfirmationRequiredNamingYes(t *testing.T) {
	g := askingPolicy(&terminal{}, "")
	g.Command = "ocel preview rm"
	g.Interactive = false

	err := g.RefuseQuestion(teardown, "before it tears the preview down")

	want := "`ocel preview rm`" + ` needs a terminal to ask "Tear down the named preview \"staging\"?" before it tears the preview down; to run it unattended, pass --yes`
	if err == nil || err.Error() != want {
		t.Fatalf("RefuseQuestion() = %v, want %s", err, want)
	}
	if got := clierror.NewRunError(err); got.GetCode() != clierror.CodeConfirmationRequired || got.GetHint() != "--yes" {
		t.Errorf("run error = %s, want code confirmation_required and hint --yes", protojson.Format(got))
	}
}

func TestAQuestionIsNotRefusedOnATerminalOrUnderYes(t *testing.T) {
	onTerminal := askingPolicy(&terminal{}, "")
	underYes := askingPolicy(&terminal{}, "")
	underYes.Interactive = false
	underYes.Yes = true

	for _, g := range []consent.Policy{onTerminal, underYes} {
		if err := g.RefuseQuestion(teardown, "before it tears the preview down"); err != nil {
			t.Errorf("RefuseQuestion() = %v with Interactive=%v Yes=%v, want nil", err, g.Interactive, g.Yes)
		}
	}
}
