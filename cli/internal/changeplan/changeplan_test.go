package changeplan_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/changeplan"
	"github.com/ocelhq/ocel/cli/internal/runui"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func mixedPlan() *contractv1.ChangePlan {
	return &contractv1.ChangePlan{
		Subject: "production",
		Groups: []*contractv1.ChangeGroup{
			{
				Kind:   "stack",
				Name:   "ocel-production-core",
				Action: contractv1.Change_ACTION_UPDATE,
				Changes: []*contractv1.Change{
					{Kind: "AWS::Lambda::Function", Name: "OcelRouterFunction", Action: contractv1.Change_ACTION_UPDATE},
					{Kind: "AWS::SecretsManager::Secret", Name: "OcelOriginSecret", Action: contractv1.Change_ACTION_REPLACE, Reason: "rotation forces replacement"},
				},
			},
			{
				Kind:    "stack",
				Name:    "ocel-production-queues",
				Feature: "queues",
				Action:  contractv1.Change_ACTION_CREATE,
				Changes: []*contractv1.Change{
					{Kind: "AWS::SQS::Queue", Name: "OcelQueue", Action: contractv1.Change_ACTION_CREATE},
					{Kind: "AWS::SQS::Queue", Name: "OcelQueueDLQ", Action: contractv1.Change_ACTION_CREATE},
				},
			},
			{
				Kind:    "stack",
				Name:    "ocel-production-isr",
				Feature: "isr",
				Action:  contractv1.Change_ACTION_DELETE,
				Reason:  "web, api were deployed against it",
				Changes: []*contractv1.Change{
					{Kind: "AWS::DynamoDB::Table", Name: "OcelRevalidationTable", Action: contractv1.Change_ACTION_DELETE},
				},
			},
			{
				Kind:    "stack",
				Name:    "ocel-production-secrets",
				Feature: "secrets",
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
	changeplan.NewPrinter(&out, runui.Presentation{}).Render("Proposed changes to the production bootstrap", mixedPlan())

	want := `Proposed changes to the production bootstrap:

~ ocel-production-core  [core]
    ~ OcelRouterFunction  AWS::Lambda::Function
    ± OcelOriginSecret    AWS::SecretsManager::Secret   — rotation forces replacement

+ ocel-production-queues  [queues]
    + OcelQueue     AWS::SQS::Queue
    + OcelQueueDLQ  AWS::SQS::Queue

– ocel-production-isr  [isr]  — web, api were deployed against it
    – OcelRevalidationTable  AWS::DynamoDB::Table

  ocel-production-secrets  [secrets]  — already current

2 to create, 1 to update, 1 to replace, 1 to delete.
`
	if out.String() != want {
		t.Errorf("Render() =\n%s\nwant\n%s", out.String(), want)
	}
}

func bootstrapPlan() *contractv1.ChangePlan {
	return &contractv1.ChangePlan{
		Subject: "production",
		Groups: []*contractv1.ChangeGroup{
			{
				Kind:   "stack",
				Name:   "aws/ocel-bootstrap",
				Action: contractv1.Change_ACTION_UPDATE,
				Changes: []*contractv1.Change{
					{Kind: "AWS::Lambda::Function", Name: "OcelRouterFunction", Action: contractv1.Change_ACTION_UPDATE},
				},
			},
			{
				Kind:    "stack",
				Name:    "aws/ocel-bootstrap-cloudflare-edge",
				Feature: "cloudflare-edge",
				Action:  contractv1.Change_ACTION_CREATE,
				Changes: []*contractv1.Change{
					{Kind: "AWS::IAM::User", Name: "EdgeUser", Action: contractv1.Change_ACTION_CREATE},
					{Kind: "AWS::Lambda::Function", Name: "TagPublisher", Action: contractv1.Change_ACTION_CREATE},
				},
			},
			{
				Kind:    "stack",
				Name:    "aws/ocel-bootstrap-isr",
				Feature: "isr",
				Action:  contractv1.Change_ACTION_KEEP,
				Reason:  "already current",
			},
			{
				Kind:    "edge",
				Name:    "cloudflare/edge",
				Feature: "cloudflare-edge",
				Action:  contractv1.Change_ACTION_CREATE,
				Changes: []*contractv1.Change{
					{Kind: "Cloudflare::R2Bucket", Name: "ocel-edge-cache", Action: contractv1.Change_ACTION_CREATE},
					{Kind: "Cloudflare::APIToken", Name: "ocel-edge-cache", Action: contractv1.Change_ACTION_CREATE},
					{Kind: "Cloudflare::Worker", Name: "ocel-deployments-store", Action: contractv1.Change_ACTION_CREATE},
					{Kind: "Cloudflare::WorkerSecret", Name: "ocel-deployments-store/BOOTSTRAP_SECRET", Action: contractv1.Change_ACTION_CREATE},
					{Kind: "Cloudflare::WorkerSubdomain", Name: "ocel-deployments-store", Action: contractv1.Change_ACTION_CREATE},
				},
			},
		},
	}
}

func TestRenderNamesTheVendorAndLeavesTheKindUnsaid(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	changeplan.NewPrinter(&out, runui.Presentation{}).Render("Proposed changes to the production bootstrap", bootstrapPlan())

	want := `Proposed changes to the production bootstrap:

~ aws/ocel-bootstrap  [core]
    ~ OcelRouterFunction  AWS::Lambda::Function

+ aws/ocel-bootstrap-cloudflare-edge  [cloudflare-edge]
    + EdgeUser      AWS::IAM::User
    + TagPublisher  AWS::Lambda::Function

  aws/ocel-bootstrap-isr  [isr]  — already current

+ cloudflare/edge  [cloudflare-edge]
    + ocel-edge-cache                          Cloudflare::R2Bucket
    + ocel-edge-cache                          Cloudflare::APIToken
    + ocel-deployments-store                   Cloudflare::Worker
    + ocel-deployments-store/BOOTSTRAP_SECRET  Cloudflare::WorkerSecret
    + ocel-deployments-store                   Cloudflare::WorkerSubdomain

7 to create, 1 to update.
`
	if out.String() != want {
		t.Errorf("Render() =\n%s\nwant\n%s", out.String(), want)
	}
	if changeplan.AllKeep(bootstrapPlan()) {
		t.Error("AllKeep() = true for a plan holding creates and an update")
	}
	if got := changeplan.ConfirmVerb(bootstrapPlan()); got != "Apply these changes" {
		t.Errorf("ConfirmVerb() = %q, want a plan mixing creates and an update to read as one", got)
	}
}

func TestRenderKeepsAnEdgeThatIsAlreadyCurrent(t *testing.T) {
	t.Parallel()

	plan := &contractv1.ChangePlan{Groups: []*contractv1.ChangeGroup{
		{Kind: "stack", Name: "aws/ocel-bootstrap", Action: contractv1.Change_ACTION_KEEP, Reason: "already current"},
		{
			Kind:    "edge",
			Name:    "cloudflare/edge",
			Feature: "cloudflare-edge",
			Action:  contractv1.Change_ACTION_KEEP,
			Reason:  "already current",
			Changes: []*contractv1.Change{
				{Kind: "Cloudflare::R2Bucket", Name: "ocel-edge-cache", Action: contractv1.Change_ACTION_KEEP, Reason: "already current"},
			},
		},
	}}
	var out bytes.Buffer
	changeplan.NewPrinter(&out, runui.Presentation{}).Render("Proposed changes to the production bootstrap", plan)

	want := `Proposed changes to the production bootstrap:

  aws/ocel-bootstrap  [core]  — already current
  cloudflare/edge  [cloudflare-edge]  — already current
`
	if out.String() != want {
		t.Errorf("Render() =\n%s\nwant\n%s", out.String(), want)
	}
	if !changeplan.AllKeep(plan) {
		t.Error("AllKeep() = false for a plan whose stacks and edge are both current")
	}
}

func TestRenderLeavesAnEdgeWithNoFeatureUntagged(t *testing.T) {
	t.Parallel()

	plan := &contractv1.ChangePlan{Groups: []*contractv1.ChangeGroup{
		{
			Kind:   "edge",
			Name:   "cloudflare/edge",
			Action: contractv1.Change_ACTION_CREATE,
			Changes: []*contractv1.Change{
				{Kind: "Cloudflare::R2Bucket", Name: "ocel-edge-cache", Action: contractv1.Change_ACTION_CREATE},
			},
		},
	}}
	var out bytes.Buffer
	changeplan.NewPrinter(&out, runui.Presentation{}).Render("Proposed changes to the production bootstrap", plan)

	if strings.Contains(out.String(), "[core]") {
		t.Errorf("Render() =\n%s\nwant an edge belonging to no feature not tagged as the core stack", out.String())
	}
}

func TestRenderCountsAChildlessGroupAsOneItem(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	changeplan.NewPrinter(&out, runui.Presentation{}).Render("Proposed changes to the preview bootstrap", &contractv1.ChangePlan{
		Groups: []*contractv1.ChangeGroup{
			{Kind: "stack", Name: "ocel-preview-core", Action: contractv1.Change_ACTION_CREATE},
			{Kind: "distribution", Name: "E1PREVIEW", Action: contractv1.Change_ACTION_DISABLE_THEN_DELETE, Slow: true},
		},
	})

	want := `Proposed changes to the preview bootstrap:

+ ocel-preview-core  [core]
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
	changeplan.NewPrinter(&out, runui.Presentation{}).Render("Proposed changes to the production bootstrap", plan)

	want := `Proposed changes to the production bootstrap:

  ocel-production-core  [core]  — already current
`
	if out.String() != want {
		t.Errorf("Render() =\n%s\nwant\n%s", out.String(), want)
	}
	if !changeplan.AllKeep(plan) {
		t.Error("AllKeep() = false, want a plan of nothing but keeps to be recognised as one")
	}
}

func TestPrintSeparatesTheDoomedFromTheKept(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	changeplan.NewPrinter(&out, runui.Presentation{}).Print("This will permanently destroy production project \"shop\"", &contractv1.ChangePlan{
		EdgeKind: "cloudflare",
		Groups: []*contractv1.ChangeGroup{
			{Kind: "edge stack", Name: "shop", Action: contractv1.Change_ACTION_DELETE},
			{Kind: "infra stack", Name: "shop--infra", Action: contractv1.Change_ACTION_DELETE, Reason: "databases and buckets, INCLUDING ALL DATA"},
			{Kind: "certificate", Name: "shop.example.com", Action: contractv1.Change_ACTION_KEEP, Reason: "you pinned this certificate"},
		},
	}, "– all stored assets belonging to this project", "This cannot be undone.")

	want := `This will permanently destroy production project "shop", fronted by the cloudflare edge:

– edge stack shop
– infra stack shop--infra  — databases and buckets, INCLUDING ALL DATA

– all stored assets belonging to this project
This cannot be undone.

Left in place:

  certificate shop.example.com  — you pinned this certificate

2 to delete.
`
	if out.String() != want {
		t.Errorf("Print() =\n%s\nwant\n%s", out.String(), want)
	}
}

func TestPrintShowsAKeptGroupThatCarriesRows(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	changeplan.NewPrinter(&out, runui.Presentation{}).Print("This will permanently destroy production project \"shop\"", &contractv1.ChangePlan{
		Groups: []*contractv1.ChangeGroup{
			{Kind: "edge stack", Name: "shop", Action: contractv1.Change_ACTION_DELETE},
			{
				Kind:   "edge",
				Name:   "cloudflare/edge",
				Action: contractv1.Change_ACTION_KEEP,
				Reason: "bootstrap-scoped",
				Changes: []*contractv1.Change{
					{Kind: "Cloudflare::Worker", Name: "ocel-preview-entry", Action: contractv1.Change_ACTION_KEEP, Reason: "every project's previews are served through it"},
				},
			},
		},
	})

	want := `This will permanently destroy production project "shop":

– edge stack shop

Left in place:

  cloudflare/edge  — bootstrap-scoped
  ocel-preview-entry  — every project's previews are served through it

1 to delete.
`
	if out.String() != want {
		t.Errorf("Print() =\n%s\nwant\n%s", out.String(), want)
	}
}

func keptGroupWithDeletes() *contractv1.ChangePlan {
	return &contractv1.ChangePlan{Groups: []*contractv1.ChangeGroup{
		{
			Kind:   "edge",
			Name:   "cloudflare/edge",
			Action: contractv1.Change_ACTION_KEEP,
			Reason: "already current",
			Changes: []*contractv1.Change{
				{Kind: "Cloudflare::WorkerRoute", Name: "*.preview.shop.com", Action: contractv1.Change_ACTION_DELETE},
				{Kind: "Cloudflare::Worker", Name: "ocel-preview-entry", Action: contractv1.Change_ACTION_KEEP, Reason: "shared with every other wildcard"},
			},
		},
	}}
}

func TestAGroupCalledKeptThatDeletesIsRenderedAsWhatItDoes(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	changeplan.NewPrinter(&out, runui.Presentation{}).Print("This will release the preview wildcard", keptGroupWithDeletes())

	want := `This will release the preview wildcard:

– cloudflare/edge
    – *.preview.shop.com  Cloudflare::WorkerRoute

Left in place:

  ocel-preview-entry  — shared with every other wildcard

1 to delete.
`
	if out.String() != want {
		t.Errorf("Print() =\n%s\nwant\n%s", out.String(), want)
	}
	if changeplan.AllKeep(keptGroupWithDeletes()) {
		t.Error("AllKeep() = true for a group called kept whose rows are deleted")
	}
	if got := changeplan.Tally(keptGroupWithDeletes()); got != "1 to delete." {
		t.Errorf("Tally() = %q, want the delete the group carries counted", got)
	}

	out.Reset()
	changeplan.NewPrinter(&out, runui.Presentation{}).Render("Proposed changes", keptGroupWithDeletes())
	if !strings.Contains(out.String(), "*.preview.shop.com") {
		t.Errorf("Render() =\n%s\nwant the row it deletes named", out.String())
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

func TestRenderNamesTheParameterGroupBareAndSpellsItsRowsOut(t *testing.T) {
	t.Parallel()

	plan := &contractv1.ChangePlan{Groups: []*contractv1.ChangeGroup{
		{Kind: "stack", Name: "aws/ocel-bootstrap", Action: contractv1.Change_ACTION_KEEP, Reason: "already current"},
		{
			Kind:   "parameters",
			Name:   "aws/parameters",
			Action: contractv1.Change_ACTION_UPDATE,
			Changes: []*contractv1.Change{
				{Kind: "AWS::SSM::Parameter", Name: "/ocel/origin/secret", Action: contractv1.Change_ACTION_KEEP, Reason: "already current"},
				{Kind: "AWS::SSM::Parameter", Name: "/ocel/edge/cloudflare/values", Action: contractv1.Change_ACTION_UPDATE, Reason: "what the edge hands back differs from what stands"},
				{Kind: "AWS::SSM::Parameter", Name: "/ocel/edge/cloudflare/credentials", Action: contractv1.Change_ACTION_CREATE},
				{Kind: "AWS::IAM::AccessKey", Name: "ocel-edge", Action: contractv1.Change_ACTION_CREATE},
			},
		},
	}}
	var out bytes.Buffer
	changeplan.NewPrinter(&out, runui.Presentation{}).Render("Proposed changes to the production bootstrap", plan)

	want := `Proposed changes to the production bootstrap:

  aws/ocel-bootstrap  [core]  — already current

~ aws/parameters
      /ocel/origin/secret                AWS::SSM::Parameter   — already current
    ~ /ocel/edge/cloudflare/values       AWS::SSM::Parameter   — what the edge hands back differs from what stands
    + /ocel/edge/cloudflare/credentials  AWS::SSM::Parameter
    + ocel-edge                          AWS::IAM::AccessKey

2 to create, 1 to update.
`
	if out.String() != want {
		t.Errorf("Render() =\n%s\nwant\n%s", out.String(), want)
	}
}
