package alb

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func stacking(t *testing.T) PulumiStacks {
	t.Helper()
	return PulumiStacks{OpenClients: googletest.Opener(googletest.Clients(""))}
}

func TestEachProjectsBindingKeepsItsStateUnderAPrefixOfItsOwn(t *testing.T) {
	t.Parallel()

	stacks := stacking(t)
	tier := environment.TierProduction
	prefixes := map[string]string{}
	for _, target := range []Target{
		{Tier: tier},
		{Tier: tier, Slug: "shop"},
		{Tier: tier, Slug: "blog"},
	} {
		config, _ := stacks.config(googletest.Clients(""), "ocel-prod-state-k3x9q2", target, "secret")
		prefixes[target.Slug] = config.Backend.URL
	}

	bucket := "gs://ocel-prod-state-k3x9q2/"
	for slug, url := range prefixes {
		if !strings.HasPrefix(url, bucket) {
			t.Errorf("the %q stack keeps state at %q, want it in the tier's own state bucket %q", slug, url, bucket)
		}
	}
	if prefixes["shop"] == prefixes["blog"] || prefixes["shop"] == prefixes[""] {
		t.Errorf("the stacks keep state at %v, want a prefix each: the state backend lists every stack under the prefix "+
			"it is opened at, so one shared prefix makes every operation cost what the account stores", prefixes)
	}
}

func TestTheFrontIsRefreshedBeforeItIsRaisedSoTheRoutesWrittenBesideItSurvive(t *testing.T) {
	t.Parallel()

	stacks := stacking(t)
	front, ref := stacks.config(googletest.Clients(""), "ocel-prod-state-k3x9q2", Target{Tier: environment.TierProduction}, "secret")
	if front.Refresh == nil || !front.Refresh(ref, pulumi.OperationProvision) {
		t.Error("the front stack is raised without a refresh, and its url map ignores changes to hostRules and pathMatchers by keeping " +
			"what state says: state that never saw the host rules a bind wrote puts every project in the tier back to unrouted")
	}
}

func TestAnALBStackIsTornDownWithoutBeingHandedTheProgramThatRaisedIt(t *testing.T) {
	t.Parallel()

	stacks := stacking(t)
	config, ref := stacks.config(googletest.Clients(""), "ocel-prod-state-k3x9q2", Target{Tier: environment.TierProduction, Slug: "shop"}, "secret")
	if _, err := pulumi.New(config).Workspace(ref, nil); err != nil {
		t.Errorf("Workspace() for a teardown = %v, want the stack ready to destroy: a teardown has no program to hand it", err)
	}
}
