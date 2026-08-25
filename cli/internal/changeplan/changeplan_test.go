package changeplan_test

import (
	"bytes"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/changeplan"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func mixedPlan() *contractv1.ChangePlan {
	return &contractv1.ChangePlan{
		Subject:  "production",
		EdgeKind: "cloudflare",
		Groups: []*contractv1.ChangeGroup{
			{
				Kind:   "stack",
				Name:   "ocel-production-core",
				Action: contractv1.Change_ACTION_UPDATE,
				Reason: "content is behind this build",
				Changes: []*contractv1.Change{
					{Kind: "AWS::Lambda::Function", Name: "OcelRouterFunction", Action: contractv1.Change_ACTION_UPDATE},
					{Kind: "AWS::SecretsManager::Secret", Name: "OcelOriginSecret", Action: contractv1.Change_ACTION_REPLACE, Reason: "rotation forces replacement"},
				},
			},
			{
				Kind:   "stack",
				Name:   "ocel-production-queues",
				Action: contractv1.Change_ACTION_CREATE,
				Reason: "queues joins the feature set",
				Changes: []*contractv1.Change{
					{Kind: "AWS::SQS::Queue", Name: "OcelQueue", Action: contractv1.Change_ACTION_CREATE},
					{Kind: "AWS::SQS::Queue", Name: "OcelDeadLetterQueue", Action: contractv1.Change_ACTION_CREATE},
				},
			},
			{
				Kind:   "stack",
				Name:   "ocel-production-isr",
				Action: contractv1.Change_ACTION_DELETE,
				Reason: "isr leaves the set; web, api were deployed against it",
				Changes: []*contractv1.Change{
					{Kind: "AWS::DynamoDB::Table", Name: "OcelRevalidationTable", Action: contractv1.Change_ACTION_DELETE},
				},
			},
			{
				Kind:    "stack",
				Name:    "ocel-production-secrets",
				Action:  contractv1.Change_ACTION_KEEP,
				Reason:  "already current",
				Changes: []*contractv1.Change{{Kind: "AWS::SecretsManager::Secret", Name: "OcelSecret", Action: contractv1.Change_ACTION_KEEP}},
			},
		},
	}
}

func TestRender(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	changeplan.Render(&out, "Proposed changes to the production bootstrap", mixedPlan())

	want := `Proposed changes to the production bootstrap (cloudflare edge):

~ stack ocel-production-core — content is behind this build
    ~ OcelRouterFunction (AWS::Lambda::Function)
    ± OcelOriginSecret (AWS::SecretsManager::Secret) — rotation forces replacement
+ stack ocel-production-queues — queues joins the feature set
    + OcelQueue (AWS::SQS::Queue)
    + OcelDeadLetterQueue (AWS::SQS::Queue)
– stack ocel-production-isr — isr leaves the set; web, api were deployed against it
    – OcelRevalidationTable (AWS::DynamoDB::Table)
  stack ocel-production-secrets — already current

2 to create, 1 to update, 1 to replace, 1 to delete.
`
	if out.String() != want {
		t.Errorf("Render() =\n%s\nwant\n%s", out.String(), want)
	}
}

func TestRenderCountsAChildlessGroupAsOneItem(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	changeplan.Render(&out, "Proposed changes to the preview bootstrap", &contractv1.ChangePlan{
		Groups: []*contractv1.ChangeGroup{
			{Kind: "stack", Name: "ocel-preview-core", Action: contractv1.Change_ACTION_CREATE, Reason: "new bootstrap"},
			{Kind: "distribution", Name: "E1PREVIEW", Action: contractv1.Change_ACTION_DISABLE_THEN_DELETE, Slow: true},
		},
	})

	want := `Proposed changes to the preview bootstrap:

+ stack ocel-preview-core — new bootstrap
– disable, then delete distribution E1PREVIEW (this one is slow)

1 to create, 1 to delete.
`
	if out.String() != want {
		t.Errorf("Render() =\n%s\nwant\n%s", out.String(), want)
	}
}

func TestRenderTalliesNothingWhenEverythingIsKept(t *testing.T) {
	t.Parallel()

	plan := &contractv1.ChangePlan{Groups: []*contractv1.ChangeGroup{
		{Kind: "stack", Name: "ocel-production-core", Action: contractv1.Change_ACTION_KEEP, Reason: "already current"},
	}}
	var out bytes.Buffer
	changeplan.Render(&out, "Proposed changes to the production bootstrap", plan)

	want := `Proposed changes to the production bootstrap:

  stack ocel-production-core — already current
`
	if out.String() != want {
		t.Errorf("Render() =\n%s\nwant\n%s", out.String(), want)
	}
	if !changeplan.AllKeep(plan) {
		t.Error("AllKeep() = false, want a plan of nothing but keeps to be recognised as one")
	}
}

func TestAllKeepIsNotAPlanWithoutGroups(t *testing.T) {
	t.Parallel()

	if changeplan.AllKeep(&contractv1.ChangePlan{}) {
		t.Error("AllKeep() = true for a provider that sent no plan; silence is not a promise that nothing changes")
	}
}

func TestConfirmVerb(t *testing.T) {
	t.Parallel()

	creates := &contractv1.ChangePlan{Groups: []*contractv1.ChangeGroup{
		{Kind: "stack", Name: "ocel-preview-core", Action: contractv1.Change_ACTION_CREATE},
	}}
	if got := changeplan.ConfirmVerb(creates); got != "Create these" {
		t.Errorf("ConfirmVerb() = %q, want a plan of pure creates to read as one", got)
	}
	if got := changeplan.ConfirmVerb(mixedPlan()); got != "Apply these changes" {
		t.Errorf("ConfirmVerb() = %q, want a mixed plan to read as one", got)
	}
}
