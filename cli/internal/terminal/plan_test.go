package terminal

import (
	"strings"
	"testing"

	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
)

func projectPlan(t *testing.T, plan *planv1.ChangePlan) string {
	t.Helper()
	return "\n" + strings.Join(planLines(Presentation{Width: defaultColumns}, plan), "\n") + "\n"
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
					{Kind: "Fake::Function", Name: "OcelDispatchFunction", Action: planv1.Change_ACTION_UPDATE},
					{Kind: "Fake::Secret", Name: "OcelOriginSecret", Action: planv1.Change_ACTION_REPLACE, Reason: "rotation forces replacement"},
				},
			},
			{
				Kind:    "stack",
				Name:    "ocel-production-queues",
				Feature: "queues",
				Action:  planv1.Change_ACTION_CREATE,
				Changes: []*planv1.Change{
					{Kind: "Fake::Queue", Name: "OcelQueue", Action: planv1.Change_ACTION_CREATE},
					{Kind: "Fake::Queue", Name: "OcelQueueDLQ", Action: planv1.Change_ACTION_CREATE},
				},
			},
			{
				Kind:    "stack",
				Name:    "ocel-production-isr",
				Feature: "isr",
				Action:  planv1.Change_ACTION_DELETE,
				Reason:  "web, api were deployed against it",
				Changes: []*planv1.Change{
					{Kind: "Fake::Table", Name: "OcelRevalidationTable", Action: planv1.Change_ACTION_DELETE},
				},
			},
			{
				Kind:    "stack",
				Name:    "ocel-production-secrets",
				Feature: "secrets",
				Action:  planv1.Change_ACTION_KEEP,
				Reason:  "already current",
				Changes: []*planv1.Change{{Kind: "Fake::Secret", Name: "OcelSecret", Action: planv1.Change_ACTION_KEEP}},
			},
		},
	}
}

func TestAPlanProjectsAsRowsOfRemoteMutationUnderOneTally(t *testing.T) {
	t.Parallel()

	want := `
Proposed changes to the production bootstrap:

~ ocel-production-core  [core]
    ~ OcelDispatchFunction  Fake::Function
    ± OcelOriginSecret      Fake::Secret   — rotation forces replacement

+ ocel-production-queues  [queues]
    + OcelQueue     Fake::Queue
    + OcelQueueDLQ  Fake::Queue

– ocel-production-isr  [isr]  — web, api were deployed against it
    – OcelRevalidationTable  Fake::Table

2 to create, 1 to update, 1 to replace, 1 to delete, 1 unchanged.
`
	if got := projectPlan(t, mixedPlan()); got != want {
		t.Errorf("projection =\n%s\nwant\n%s", got, want)
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

	got := strings.Join(planLines(Presentation{Color: true, Width: defaultColumns}, &planv1.ChangePlan{
		Headline: "Proposed changes to the production bootstrap",
		Groups: []*planv1.ChangeGroup{
			{
				Kind:    "stack",
				Name:    "ocel-production-queues",
				Feature: "queues",
				Action:  planv1.Change_ACTION_CREATE,
				Changes: []*planv1.Change{{Kind: "Fake::Queue", Name: "OcelQueue", Action: planv1.Change_ACTION_CREATE}},
			},
			{
				Kind:    "stack",
				Name:    "ocel-production-isr",
				Feature: "isr",
				Action:  planv1.Change_ACTION_DELETE,
				Reason:  "web, api were deployed against it",
			},
		},
	}), "\n")

	for _, want := range []string{
		"\x1b[32m+\x1b[0m \x1b[1mocel-production-queues\x1b[22m  \x1b[90m[queues]\x1b[0m",
		"\x1b[32m+\x1b[0m OcelQueue  \x1b[90mFake::Queue\x1b[0m",
		"\x1b[31m–\x1b[0m \x1b[1mocel-production-isr\x1b[22m  \x1b[90m[isr]\x1b[0m\x1b[90m  — web, api were deployed against it\x1b[0m",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("projection = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "\x1b[32m1 to create") {
		t.Errorf("projection = %q, want the tally left in the default colour", got)
	}
}

func TestAPlanOffATerminalHasNoEscapeCodes(t *testing.T) {
	t.Parallel()

	got := projectPlan(t, mixedPlan())
	if strings.Contains(got, "\x1b[") {
		t.Errorf("projection = %q, want no escape codes where the run has no colour", got)
	}
}

func TestAPlanIncludesItsNotesAndTheEdgeFrontingIt(t *testing.T) {
	t.Parallel()

	want := `
This will permanently destroy production project "shop", fronted by the relay edge:

– edge stack shop
– infra stack shop--infra  — databases and buckets, INCLUDING ALL DATA

– all stored assets belonging to this project
This cannot be undone.

2 to delete, 1 unchanged.
`
	got := projectPlan(t, &planv1.ChangePlan{
		Headline: `This will permanently destroy production project "shop"`,
		EdgeKind: "relay",
		Notes: []*planv1.Note{
			{Action: planv1.Change_ACTION_DELETE, Text: "all stored assets belonging to this project"},
			{Text: "This cannot be undone."},
		},
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
			{Kind: "stack", Name: "fake/ocel-bootstrap", Action: planv1.Change_ACTION_KEEP, Reason: "already current"},
			{
				Kind:    "edge",
				Name:    "relay/edge",
				Feature: "relay-edge",
				Action:  planv1.Change_ACTION_KEEP,
				Reason:  "already current",
				Changes: []*planv1.Change{{Kind: "Fake::Bucket", Name: "ocel-edge-cache", Action: planv1.Change_ACTION_KEEP}},
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

~ relay/edge
    – *.preview.shop.com  Fake::Route

1 to delete, 1 unchanged.
`
	got := projectPlan(t, &planv1.ChangePlan{
		Headline: "This will release the preview wildcard",
		Groups: []*planv1.ChangeGroup{
			{
				Kind:   "edge",
				Name:   "relay/edge",
				Action: planv1.Change_ACTION_KEEP,
				Reason: "already current",
				Changes: []*planv1.Change{
					{Kind: "Fake::Route", Name: "*.preview.shop.com", Action: planv1.Change_ACTION_DELETE},
					{Kind: "Fake::Worker", Name: "ocel-preview-entry", Action: planv1.Change_ACTION_KEEP, Reason: "shared with every other wildcard"},
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
– disable, then delete distribution E1PREVIEW (slow)

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

func TestAnAdoptedRowIsShownWithWhyAndCountedApartFromWork(t *testing.T) {
	t.Parallel()

	adopting := func(core planv1.Change_Action, rows ...*planv1.Change) *planv1.ChangePlan {
		return &planv1.ChangePlan{
			Headline: "Proposed changes to the production bootstrap",
			Groups:   []*planv1.ChangeGroup{{Kind: "stack", Name: "fake/ada@box", Action: core, Changes: rows}},
		}
	}
	engine := &planv1.Change{Kind: "docker:engine", Name: "docker", Action: planv1.Change_ACTION_ADOPT, Reason: "docker 28.3.1, not managed by ocel: upgrading it is yours"}
	dir := &planv1.Change{Kind: "fs:dir", Name: "/etc/ocel", Action: planv1.Change_ACTION_CREATE}
	kept := &planv1.Change{Kind: "fs:dir", Name: "/var/lib/ocel", Action: planv1.Change_ACTION_KEEP}

	fresh := `
Proposed changes to the production bootstrap:

+ fake/ada@box  [core]
    + /etc/ocel     fs:dir
    = adopt docker  docker:engine   — docker 28.3.1, not managed by ocel: upgrading it is yours

1 to create, 1 adopted.
`
	if got := projectPlan(t, adopting(planv1.Change_ACTION_CREATE, dir, engine)); got != fresh {
		t.Errorf("projection =\n%s\nwant\n%s", got, fresh)
	}

	unchanged := adopting(planv1.Change_ACTION_KEEP, kept, engine)
	want := `
Proposed changes to the production bootstrap:

  fake/ada@box  [core]
    = adopt docker  docker:engine   — docker 28.3.1, not managed by ocel: upgrading it is yours

1 adopted, 1 unchanged.
`
	if got := projectPlan(t, unchanged); got != want {
		t.Errorf("projection =\n%s\nwant\n%s", got, want)
	}
}

func TestAGroupIsRolledUpByTheRuleTheProviderRollsItsOwnGroupsUpBy(t *testing.T) {
	t.Parallel()

	rows := func(actions ...planv1.Change_Action) []*planv1.Change {
		changes := make([]*planv1.Change, 0, len(actions))
		for at, action := range actions {
			changes = append(changes, &planv1.Change{Kind: "fs:dir", Name: "/etc/ocel/" + string(rune('a'+at)), Action: action})
		}
		return changes
	}
	for name, tc := range map[string]struct {
		changes []*planv1.Change
		want    planv1.Change_Action
	}{
		"a create beside an adopt": {rows(planv1.Change_ACTION_CREATE, planv1.Change_ACTION_ADOPT), planv1.Change_ACTION_UPDATE},
		"a create beside a keep":   {rows(planv1.Change_ACTION_CREATE, planv1.Change_ACTION_KEEP), planv1.Change_ACTION_UPDATE},
		"a delete beside an adopt": {rows(planv1.Change_ACTION_DELETE, planv1.Change_ACTION_ADOPT), planv1.Change_ACTION_UPDATE},
		"an adopt beside a keep":   {rows(planv1.Change_ACTION_ADOPT, planv1.Change_ACTION_KEEP), planv1.Change_ACTION_KEEP},
		"creates alone":            {rows(planv1.Change_ACTION_CREATE, planv1.Change_ACTION_CREATE), planv1.Change_ACTION_CREATE},
	} {
		for _, sent := range []planv1.Change_Action{planv1.Change_ACTION_UNSPECIFIED, planv1.Change_ACTION_KEEP} {
			shown, _ := readPlan(&planv1.ChangePlan{Groups: []*planv1.ChangeGroup{{Kind: "stack", Name: "core", Action: sent, Changes: tc.changes}}})
			if len(shown) == 1 && shown[0].GetAction() != tc.want {
				t.Errorf("%s, sent as %s, is shown as %s, want %s: the provider rolls the same rows up to %s", name, sent, shown[0].GetAction(), tc.want, tc.want)
			}
		}
	}
}
