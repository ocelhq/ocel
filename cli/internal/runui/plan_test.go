package runui

import (
	"strings"
	"testing"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
)

func projectPlan(t *testing.T, plan *planv1.ChangePlan) string {
	t.Helper()
	p := newProjector(Presentation{Format: FormatHuman, Width: defaultWidth})
	lines := p.project(&streamv1.RunEvent{Event: &streamv1.RunEvent_Plan{Plan: plan}})
	return strings.Join(lines, "\n")
}

func mixedPlan() *planv1.ChangePlan {
	return &planv1.ChangePlan{
		Subject:  "production",
		Headline: "Proposed changes to the production bootstrap",
		Groups: []*planv1.ChangeGroup{
			{
				Kind:   "stack",
				Name:   "ocel-production-core",
				Action: planv1.Change_ACTION_UPDATE,
				Changes: []*planv1.Change{
					{Kind: "AWS::Lambda::Function", Name: "OcelRouterFunction", Action: planv1.Change_ACTION_UPDATE},
					{Kind: "AWS::SecretsManager::Secret", Name: "OcelOriginSecret", Action: planv1.Change_ACTION_REPLACE, Reason: "rotation forces replacement"},
				},
			},
			{
				Kind:    "stack",
				Name:    "ocel-production-queues",
				Feature: "queues",
				Action:  planv1.Change_ACTION_CREATE,
				Changes: []*planv1.Change{
					{Kind: "AWS::SQS::Queue", Name: "OcelQueue", Action: planv1.Change_ACTION_CREATE},
					{Kind: "AWS::SQS::Queue", Name: "OcelQueueDLQ", Action: planv1.Change_ACTION_CREATE},
				},
			},
			{
				Kind:    "stack",
				Name:    "ocel-production-isr",
				Feature: "isr",
				Action:  planv1.Change_ACTION_DELETE,
				Reason:  "web, api were deployed against it",
				Changes: []*planv1.Change{
					{Kind: "AWS::DynamoDB::Table", Name: "OcelRevalidationTable", Action: planv1.Change_ACTION_DELETE},
				},
			},
			{
				Kind:    "stack",
				Name:    "ocel-production-secrets",
				Feature: "secrets",
				Action:  planv1.Change_ACTION_KEEP,
				Reason:  "already current",
				Changes: []*planv1.Change{{Kind: "AWS::SecretsManager::Secret", Name: "OcelSecret", Action: planv1.Change_ACTION_KEEP}},
			},
		},
	}
}

func TestAPlanProjectsAsRowsOfRemoteMutationUnderOneTally(t *testing.T) {
	t.Parallel()

	want := `
Proposed changes to the production bootstrap:

~ ocel-production-core  [core]
    ~ OcelRouterFunction  AWS::Lambda::Function
    ± OcelOriginSecret    AWS::SecretsManager::Secret   — rotation forces replacement

+ ocel-production-queues  [queues]
    + OcelQueue     AWS::SQS::Queue
    + OcelQueueDLQ  AWS::SQS::Queue

– ocel-production-isr  [isr]  — web, api were deployed against it
    – OcelRevalidationTable  AWS::DynamoDB::Table

2 to create, 1 to update, 1 to replace, 1 to delete, 1 unchanged.
`
	if got := projectPlan(t, mixedPlan()); got != want {
		t.Errorf("projection =\n%s\nwant\n%s", got, want)
	}
}

func TestTheConfirmationNamesWhatThePlanActuallyDoes(t *testing.T) {
	t.Parallel()

	creates := &planv1.ChangePlan{Groups: []*planv1.ChangeGroup{
		{Kind: "stack", Name: "ocel-preview-core", Action: planv1.Change_ACTION_CREATE},
		{Kind: "stack", Name: "ocel-preview-isr", Action: planv1.Change_ACTION_KEEP},
	}}
	if got := ConfirmVerb(creates); got != "Create these" {
		t.Errorf("ConfirmVerb() = %q, want a plan that only creates to read as one", got)
	}
	if got := ConfirmVerb(mixedPlan()); got != "Apply these changes" {
		t.Errorf("ConfirmVerb() = %q, want a mixed plan to read as one", got)
	}
	if !Mutates(mixedPlan()) {
		t.Error("Mutates() = false for a plan holding creates, an update and a delete")
	}
	if Mutates(&planv1.ChangePlan{Groups: []*planv1.ChangeGroup{
		{Kind: "stack", Name: "ocel-preview-core", Action: planv1.Change_ACTION_KEEP},
	}}) {
		t.Error("Mutates() = true for a plan of nothing but keeps")
	}
}

func TestAnActionThisCLIDoesNotKnowReadsAsASentence(t *testing.T) {
	t.Parallel()

	got := projectPlan(t, &planv1.ChangePlan{
		Headline: "This will permanently destroy production project \"shop\"",
		Groups: []*planv1.ChangeGroup{
			{Kind: "certificate", Name: "shop.example.com", Action: planv1.Change_Action(97)},
		},
	})
	if !strings.Contains(got, "an action this CLI does not know") || !strings.Contains(got, "certificate shop.example.com") {
		t.Errorf("projection = %q, want the unknown action named before the resource", got)
	}
}

func TestAPlanPaintsTheSigilAndDimsWhatSaysWhy(t *testing.T) {
	t.Parallel()

	p := newProjector(Presentation{Format: FormatHuman, Color: true, Width: defaultWidth})
	got := strings.Join(p.project(&streamv1.RunEvent{Event: &streamv1.RunEvent_Plan{Plan: &planv1.ChangePlan{
		Headline: "Proposed changes to the production bootstrap",
		Groups: []*planv1.ChangeGroup{
			{
				Kind:    "stack",
				Name:    "ocel-production-queues",
				Feature: "queues",
				Action:  planv1.Change_ACTION_CREATE,
				Changes: []*planv1.Change{{Kind: "AWS::SQS::Queue", Name: "OcelQueue", Action: planv1.Change_ACTION_CREATE}},
			},
			{
				Kind:    "stack",
				Name:    "ocel-production-isr",
				Feature: "isr",
				Action:  planv1.Change_ACTION_DELETE,
				Reason:  "web, api were deployed against it",
			},
		},
	}}}), "\n")

	for _, want := range []string{
		"\x1b[32m+\x1b[0m \x1b[1mocel-production-queues\x1b[22m  \x1b[2m[queues]\x1b[22m",
		"\x1b[32m+\x1b[0m OcelQueue  \x1b[2mAWS::SQS::Queue\x1b[22m",
		"\x1b[31m–\x1b[0m \x1b[1mocel-production-isr\x1b[22m  \x1b[2m[isr]\x1b[22m\x1b[2m  — web, api were deployed against it\x1b[22m",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("projection = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "\x1b[32m1 to create") {
		t.Errorf("projection = %q, want the tally left in the default colour", got)
	}
}

func TestAPlanOffATerminalCarriesNoEscapeCodes(t *testing.T) {
	t.Parallel()

	got := projectPlan(t, mixedPlan())
	if strings.Contains(got, "\x1b[") {
		t.Errorf("projection = %q, want no escape codes where the run has no colour", got)
	}
}

func TestAPlanCarriesItsNotesAndTheEdgeFrontingIt(t *testing.T) {
	t.Parallel()

	want := `
This will permanently destroy production project "shop", fronted by the cloudflare edge:

– edge stack shop
– infra stack shop--infra  — databases and buckets, INCLUDING ALL DATA

– all stored assets belonging to this project
This cannot be undone.

2 to delete, 1 unchanged.
`
	got := projectPlan(t, &planv1.ChangePlan{
		Headline: `This will permanently destroy production project "shop"`,
		EdgeKind: "cloudflare",
		Notes:    []string{"– all stored assets belonging to this project", "This cannot be undone."},
		Groups: []*planv1.ChangeGroup{
			{Kind: "edge stack", Name: "shop", Action: planv1.Change_ACTION_DELETE},
			{Kind: "infra stack", Name: "shop--infra", Action: planv1.Change_ACTION_DELETE, Reason: "databases and buckets, INCLUDING ALL DATA"},
			{Kind: "certificate", Name: "shop.example.com", Action: planv1.Change_ACTION_KEEP, Reason: "you pinned this certificate"},
		},
	})
	if got != want {
		t.Errorf("projection =\n%s\nwant\n%s", got, want)
	}
}

func TestAPlanThatKeepsEverythingIsAllTally(t *testing.T) {
	t.Parallel()

	want := `
Proposed changes to the production bootstrap:

2 unchanged.
`
	got := projectPlan(t, &planv1.ChangePlan{
		Headline: "Proposed changes to the production bootstrap",
		Groups: []*planv1.ChangeGroup{
			{Kind: "stack", Name: "aws/ocel-bootstrap", Action: planv1.Change_ACTION_KEEP, Reason: "already current"},
			{
				Kind:    "edge",
				Name:    "cloudflare/edge",
				Feature: "cloudflare-edge",
				Action:  planv1.Change_ACTION_KEEP,
				Reason:  "already current",
				Changes: []*planv1.Change{{Kind: "Cloudflare::R2Bucket", Name: "ocel-edge-cache", Action: planv1.Change_ACTION_KEEP}},
			},
		},
	})
	if got != want {
		t.Errorf("projection =\n%s\nwant\n%s", got, want)
	}
}

func TestAGroupCalledKeptThatDeletesIsRenderedAsWhatItDoes(t *testing.T) {
	t.Parallel()

	want := `
This will release the preview wildcard:

– cloudflare/edge
    – *.preview.shop.com  Cloudflare::WorkerRoute

1 to delete, 1 unchanged.
`
	got := projectPlan(t, &planv1.ChangePlan{
		Headline: "This will release the preview wildcard",
		Groups: []*planv1.ChangeGroup{
			{
				Kind:   "edge",
				Name:   "cloudflare/edge",
				Action: planv1.Change_ACTION_KEEP,
				Reason: "already current",
				Changes: []*planv1.Change{
					{Kind: "Cloudflare::WorkerRoute", Name: "*.preview.shop.com", Action: planv1.Change_ACTION_DELETE},
					{Kind: "Cloudflare::Worker", Name: "ocel-preview-entry", Action: planv1.Change_ACTION_KEEP, Reason: "shared with every other wildcard"},
				},
			},
		},
	})
	if got != want {
		t.Errorf("projection =\n%s\nwant\n%s", got, want)
	}
}

func TestAChildlessGroupIsOneRowAndOneItemOfTheTally(t *testing.T) {
	t.Parallel()

	want := `
Proposed changes to the preview bootstrap:

+ ocel-preview-core  [core]
– disable, then delete distribution E1PREVIEW (this one is slow)

1 to create, 1 to delete.
`
	got := projectPlan(t, &planv1.ChangePlan{
		Headline: "Proposed changes to the preview bootstrap",
		Groups: []*planv1.ChangeGroup{
			{Kind: "stack", Name: "ocel-preview-core", Action: planv1.Change_ACTION_CREATE},
			{Kind: "distribution", Name: "E1PREVIEW", Action: planv1.Change_ACTION_DISABLE_THEN_DELETE, Slow: true},
		},
	})
	if got != want {
		t.Errorf("projection =\n%s\nwant\n%s", got, want)
	}
}
