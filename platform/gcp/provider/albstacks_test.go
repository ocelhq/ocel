package gcp

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func stacking(t *testing.T) albStacks {
	t.Helper()
	return albStacks{p: pushing(t, "")}
}

func TestEachProjectsBindingKeepsItsStateUnderAPrefixOfItsOwn(t *testing.T) {
	t.Parallel()

	stacks := stacking(t)
	tier := environment.TierProduction
	prefixes := map[string]string{}
	for _, target := range []alb.Target{
		{Tier: tier},
		{Tier: tier, Slug: "shop"},
		{Tier: tier, Slug: "blog"},
	} {
		config, _ := stacks.config(stacks.p.resolved, target, "secret", nil)
		prefixes[target.Slug] = config.Backend.URL
	}

	bucket := "gs://" + stacking(t).p.resolved.StateBucket(tier) + "/"
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
	front, spec := stacks.config(stacks.p.resolved, alb.Target{Tier: environment.TierProduction}, "secret", nil)
	if front.Refresh == nil || !front.Refresh(spec.Ref, pulumi.OperationProvision) {
		t.Error("the front stack is raised without a refresh, and its url map ignores changes to hostRules and pathMatchers by keeping " +
			"what state says: state that never saw the host rules a bind wrote puts every project in the tier back to unrouted")
	}
}
