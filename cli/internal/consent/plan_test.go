package consent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/consent"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
)

func keepingPlan() *planv1.ChangePlan {
	return &planv1.ChangePlan{Groups: []*planv1.ChangeGroup{
		{Kind: "stack", Name: "aws/ocel-bootstrap", Action: planv1.Change_ACTION_KEEP, Reason: "already current"},
	}}
}

func mutatingPlan() *planv1.ChangePlan {
	return &planv1.ChangePlan{Groups: []*planv1.ChangeGroup{
		{Kind: "edge", Name: "cloudflare/edge", Action: planv1.Change_ACTION_CREATE},
		{Kind: "stack", Name: "aws/ocel-bootstrap", Action: planv1.Change_ACTION_KEEP, Reason: "already current"},
	}}
}

func TestAPlanThatChangesNothingRaisesNoGateToConsentTo(t *testing.T) {
	term := &terminal{}
	g := askingGate(term, "n\n")
	g.Class = consent.PlanFirst

	granted, err := g.Consent(context.Background(), spanOn(t, term), keepingPlan(), "Apply these changes?")
	if err != nil || !granted {
		t.Errorf("Consent() = %v, %v, want a plan of nothing but keeps to have nothing to consent to", granted, err)
	}
	if strings.Contains(term.written(), "Apply these changes?") {
		t.Errorf("written = %q, want no question where the plan shows no change", term.written())
	}
}

func TestAPlanThatChangesSomethingStillRaisesTheGate(t *testing.T) {
	term := &terminal{}
	g := askingGate(term, "n\n")
	g.Class = consent.PlanFirst

	granted, err := g.Consent(context.Background(), spanOn(t, term), mutatingPlan(), "Apply these changes?")
	if err != nil || granted {
		t.Errorf("Consent() = %v, %v, want one create among the keeps to keep the gate up and the no to withhold it", granted, err)
	}
	if !strings.Contains(term.written(), "Apply these changes?") {
		t.Errorf("written = %q, want the plan's own confirmation put to the terminal", term.written())
	}
}

func mixedPlan() *planv1.ChangePlan {
	return &planv1.ChangePlan{Groups: []*planv1.ChangeGroup{
		{Kind: "stack", Name: "ocel-production-core", Action: planv1.Change_ACTION_UPDATE, Changes: []*planv1.Change{
			{Kind: "AWS::Lambda::Function", Name: "OcelDispatchFunction", Action: planv1.Change_ACTION_UPDATE},
			{Kind: "AWS::SecretsManager::Secret", Name: "OcelOriginSecret", Action: planv1.Change_ACTION_REPLACE},
		}},
		{Kind: "stack", Name: "ocel-production-queues", Action: planv1.Change_ACTION_CREATE, Changes: []*planv1.Change{
			{Kind: "AWS::SQS::Queue", Name: "OcelQueue", Action: planv1.Change_ACTION_CREATE},
		}},
		{Kind: "stack", Name: "ocel-production-isr", Action: planv1.Change_ACTION_DELETE},
	}}
}

func TestTheConfirmationNamesWhatThePlanActuallyDoes(t *testing.T) {
	creates := &planv1.ChangePlan{Groups: []*planv1.ChangeGroup{
		{Kind: "stack", Name: "ocel-preview-core", Action: planv1.Change_ACTION_CREATE},
		{Kind: "stack", Name: "ocel-preview-isr", Action: planv1.Change_ACTION_KEEP},
	}}
	if got := consent.ConfirmVerb(creates); got != "Create these" {
		t.Errorf("ConfirmVerb() = %q, want a plan that only creates to read as one", got)
	}
	if got := consent.ConfirmVerb(mixedPlan()); got != "Apply these changes" {
		t.Errorf("ConfirmVerb() = %q, want a mixed plan to read as one", got)
	}
	if !consent.Mutates(mixedPlan()) {
		t.Error("Mutates() = false for a plan containing creates, an update and a delete")
	}
	if consent.Mutates(&planv1.ChangePlan{Groups: []*planv1.ChangeGroup{
		{Kind: "stack", Name: "ocel-preview-core", Action: planv1.Change_ACTION_KEEP},
	}}) {
		t.Error("Mutates() = true for a plan of nothing but keeps")
	}
}

func TestAnAdoptedRowIsNotWorkToConsentTo(t *testing.T) {
	adopting := func(core planv1.Change_Action, rows ...*planv1.Change) *planv1.ChangePlan {
		return &planv1.ChangePlan{Groups: []*planv1.ChangeGroup{{Kind: "stack", Name: "vps/ada@box", Action: core, Changes: rows}}}
	}
	engine := &planv1.Change{Kind: "docker:engine", Name: "docker", Action: planv1.Change_ACTION_ADOPT}
	dir := &planv1.Change{Kind: "fs:dir", Name: "/etc/ocel", Action: planv1.Change_ACTION_CREATE}
	kept := &planv1.Change{Kind: "fs:dir", Name: "/var/lib/ocel", Action: planv1.Change_ACTION_KEEP}

	if consent.Mutates(adopting(planv1.Change_ACTION_KEEP, kept, engine)) {
		t.Error("Mutates() = true for a plan that only adopts what is already installed, and every re-run would ask consent for nothing")
	}
	if got := consent.ConfirmVerb(adopting(planv1.Change_ACTION_CREATE, dir, engine)); got != "Create these" {
		t.Errorf("ConfirmVerb() = %q, want a plan that creates and adopts to read as one that creates", got)
	}
}

func TestAGroupWithNoActionWhoseRowsAreAllKeptIsNotWork(t *testing.T) {
	if consent.Mutates(&planv1.ChangePlan{Groups: []*planv1.ChangeGroup{
		{Kind: "stack", Name: "ocel-preview-core", Action: planv1.Change_ACTION_UNSPECIFIED, Changes: []*planv1.Change{
			{Name: "a", Action: planv1.Change_ACTION_KEEP},
		}},
	}}) {
		t.Error("Mutates() = true for a group whose only rows are kept, want what the plan tallies as unchanged to raise no gate")
	}
}
