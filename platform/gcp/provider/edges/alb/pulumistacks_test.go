package alb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/pkg/refusal"
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

func TestTheFrontIsRefreshedBeforeItIsDestroyedOnlyWhenTheChecksAreNotSkipped(t *testing.T) {
	t.Parallel()

	for skipChecks, wantRefreshed := range map[bool]bool{false: true, true: false} {
		stacks := stacking(t)
		stacks.SkipChecks = skipChecks
		front, ref := stacks.config(googletest.Clients(""), "ocel-prod-state-k3x9q2", Target{Tier: environment.TierProduction}, "secret")
		if refreshed := front.Refresh(ref, pulumi.OperationDestroy); refreshed != wantRefreshed {
			t.Errorf("refresh before the front's destroy with the checks skipped %t = %t, want %t: the harness skips them to trade the drift check for speed", skipChecks, refreshed, wantRefreshed)
		}
		if !front.Refresh(ref, pulumi.OperationProvision) {
			t.Errorf("refresh before the front is raised with the checks skipped %t = false, want true: it keeps the routes binds wrote beside it", skipChecks)
		}
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

func unsealedStacks(t *testing.T) PulumiStacks {
	t.Helper()
	secrets := googletest.NewSecrets()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secrets.Serve(t, w, r)
	}))
	t.Cleanup(server.Close)
	return PulumiStacks{OpenClients: googletest.Opener(googletest.Clients(server.URL))}
}

func TestATierWhosePassphraseWasNeverMadeHasNoLoadBalancerInstalledOrHostnameBound(t *testing.T) {
	t.Parallel()

	balancer := New(Deps{Stacks: unsealedStacks(t), Entries: newWorld()})
	installed, err := balancer.bootstrapInstalled(context.Background(), environment.TierProduction)
	if err != nil || installed {
		t.Errorf("bootstrapInstalled() = %v, %v, want false and no error: no state is sealed under a passphrase that was never made, "+
			"and refusing leaves a bootstrap that failed before making it unable to be read or resumed", installed, err)
	}
	bound, err := balancer.boundHostnames(context.Background(), environment.TierProduction)
	if err != nil || len(bound) > 0 {
		t.Errorf("boundHostnames() = %v, %v, want none and no error: no load balancer exists to bind a hostname to", bound, err)
	}
}

func TestATierWhosePassphraseWasNeverMadeRefusesToRaiseItsLoadBalancer(t *testing.T) {
	t.Parallel()

	target := Target{Tier: environment.TierProduction}
	_, err := unsealedStacks(t).Up(context.Background(), target, nil, progress.Discard())
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Errorf("Up() = %v, want a %s refusal naming `ocel bootstrap`: it seals state under the passphrase", err, refusal.CodeNotReady)
	}
}

func TestATierWhosePassphraseWasNeverMadeHasNoLoadBalancerToDestroy(t *testing.T) {
	t.Parallel()

	target := Target{Tier: environment.TierProduction}
	if err := unsealedStacks(t).Destroy(context.Background(), target, progress.Discard()); err != nil {
		t.Errorf("Destroy() = %v, want nil: no state is sealed under a passphrase that was never made, and refusing leaves "+
			"a bootstrap that failed before making it, or a removal retried after deleting it, unable to be removed", err)
	}
}
