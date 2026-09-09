package gcp

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	kitpulumi "github.com/ocelhq/ocel/pkg/providerkit/pulumi"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func stacking(t *testing.T) albStacks {
	t.Helper()
	return albStacks{p: pushing(t, "")}
}

func TestEachProjectsBindingKeepsItsStateUnderAPrefixOfItsOwn(t *testing.T) {
	t.Parallel()

	stacks := stacking(t)
	class := providerkit.ClassProduction
	prefixes := map[string]string{}
	for _, target := range []alb.Target{
		{Class: class},
		{Class: class, Slug: "shop"},
		{Class: class, Slug: "blog"},
	} {
		config, _ := stacks.config(target, "secret", nil)
		prefixes[target.Slug] = config.Access.BackendURL
	}

	bucket := "gs://" + stacking(t).p.Names().StateBucket(class) + "/"
	for slug, url := range prefixes {
		if !strings.HasPrefix(url, bucket) {
			t.Errorf("the %q stack keeps state at %q, want it in the class's own state bucket %q", slug, url, bucket)
		}
	}
	if prefixes["shop"] == prefixes["blog"] || prefixes["shop"] == prefixes[""] {
		t.Errorf("the stacks keep state at %v, want a prefix each: the state backend lists every stack under the prefix "+
			"it is opened at, so one shared prefix makes every operation cost what the account holds", prefixes)
	}
}

func TestTheFrontIsRefreshedBeforeItIsRaisedSoTheRoutesWrittenBesideItSurvive(t *testing.T) {
	t.Parallel()

	stacks := stacking(t)
	front, plan := stacks.config(alb.Target{Class: providerkit.ClassProduction}, "secret", nil)
	if front.Refresh == nil || !front.Refresh(plan.Ref, kitpulumi.OpProvision) {
		t.Error("the front stack is raised without a refresh, and its url map ignores changes to hostRules and pathMatchers by holding " +
			"what state says: state that never saw the host rules a bind wrote puts every project in the class back to unrouted")
	}
}
