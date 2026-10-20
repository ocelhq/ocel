package gcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const (
	computeAccount   = "514957526729-compute@developer.gserviceaccount.com"
	deployerAccount  = "deployer@acme-prod.iam.gserviceaccount.com"
	connectorAccount = "ocel-connector@acme-prod.iam.gserviceaccount.com"
	realtimeAccount  = "ocel-prod-realtime@acme-prod.iam.gserviceaccount.com"
	appAccount       = "ocel-app-0123456789@acme-prod.iam.gserviceaccount.com"
)

var outsideAccounts = []string{computeAccount, deployerAccount, connectorAccount}

func accountParent(email string) string {
	return googletest.AccountTagParent(strings.TrimSuffix(email, "@acme-prod.iam.gserviceaccount.com"))
}

func projectHoldingAccounts(t *testing.T) (*googletest.IAM, *gcloud.Clients, *googletest.TierTags) {
	t.Helper()
	server := googletest.GrantedIAM()
	server.Listed = slices.Concat(outsideAccounts, []string{realtimeAccount, appAccount})
	c := server.Open(t)
	tags := googletest.NewTieredTags(t)
	tags.Tagged(c)
	return server, c, tags
}

func outsideTag(c *gcloud.Clients) bootstrap.Resource {
	return bootstrap.Resource{Kind: bootstrap.KindOutsideTag, Name: c.OutsideTagValuePath(), Shared: true}
}

func TestABootstrapBindsTheOutsideValueToEveryServiceAccountOcelDoesNotMake(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, c, tags := projectHoldingAccounts(t)
	b := bootstrap.Engine{Clients: c}
	read := bootstrap.Survey{Tier: environment.TierProduction, Names: c.Names, Held: map[string]bool{}, Mending: map[string]string{}}
	target := outsideTag(c)

	found, err := b.PresenceOf(ctx, read, target)
	if err != nil {
		t.Fatal(err)
	}
	for _, email := range outsideAccounts {
		if !strings.Contains(found.Mends, email) {
			t.Errorf("the plan mends the outside tag for %q, want it to name %s, which no ocel deploy makes", found.Mends, email)
		}
	}
	for _, email := range []string{realtimeAccount, appAccount} {
		if strings.Contains(found.Mends, email) {
			t.Errorf("the plan names %s among the accounts ocel does not make, and a deploy binds its own accounts to their tier", email)
		}
	}

	read.Held[target.ID()], read.Mending[target.ID()] = true, found.Mends
	if err := b.Provision(ctx, read, target, nil); err != nil {
		t.Fatal(err)
	}
	outside := tags.Values[c.OutsideTagValuePath()].Name
	for _, email := range outsideAccounts {
		if got := tags.Bound(accountParent(email)); !slices.Equal(got, []string{outside}) {
			t.Errorf("%s carries %v, want the outside value", email, got)
		}
	}
	for _, email := range []string{realtimeAccount, appAccount} {
		if got := tags.Bound(accountParent(email)); len(got) != 0 {
			t.Errorf("%s carries %v, want nothing bound by the outside tag: its own tier binds it", email, got)
		}
	}
	again, err := b.PresenceOf(ctx, read, target)
	if err != nil || again.Mends != "" {
		t.Errorf("the outside tag reads %+v, %v once bound, want current", again, err)
	}
}

func TestRemovingATierKeepsTheOutsideValueOnTheProjectsAccounts(t *testing.T) {
	t.Parallel()
	_, c, _ := projectHoldingAccounts(t)
	target := outsideTag(c)
	read := bootstrap.Survey{Tier: environment.TierPreview, Names: c.Names, Held: map[string]bool{target.ID(): true}}

	taking := bootstrap.Removing(read, target)

	if taking.Action != provider.ActionKeep || taking.Reason != bootstrap.ReasonOutsideKept {
		t.Errorf("removing a tier does %s to the outside tag (%q), want it kept: %s", taking.Action, taking.Reason, bootstrap.ReasonOutsideKept)
	}
}

func preflightOf(tier environment.Tier) provider.DeployPreflight {
	return provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: tier}}
}

func TestADeployRefusesToRunBesideAServiceAccountNoTierTagFences(t *testing.T) {
	t.Parallel()
	_, c, tags := projectHoldingAccounts(t)
	p := pushing(t, c.Endpoint)
	tags.Tagged(p.resolved)

	_, err := p.PlanPreflight(context.Background(), preflightOf(environment.TierPreview))

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("PlanPreflight() beside untagged accounts = %v, want a %s refusal", err, refusal.CodeNotReady)
	}
	for _, want := range []string{computeAccount, "ocel bootstrap preview", "acme-prod/ocel-tier/outside"} {
		if !strings.Contains(refused.Message, want) {
			t.Errorf("the refusal %q does not name %s", refused.Message, want)
		}
	}
	if err := p.ApplyPreflight(context.Background(), preflightOf(environment.TierPreview)); !errors.As(err, &refused) {
		t.Errorf("ApplyPreflight() beside untagged accounts = %v, want the same refusal", err)
	}
}

func TestADeployRunsOnceEveryAccountOcelDoesNotMakeCarriesATierTag(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, c, tags := projectHoldingAccounts(t)
	p := pushing(t, c.Endpoint)
	tags.Tagged(p.resolved)
	b := bootstrap.Engine{Clients: p.resolved}
	read := bootstrap.Survey{Tier: environment.TierPreview, Names: c.Names, Held: map[string]bool{}, Mending: map[string]string{}}
	target := outsideTag(c)
	read.Held[target.ID()], read.Mending[target.ID()] = true, "untagged"
	if err := b.Provision(ctx, read, target, nil); err != nil {
		t.Fatal(err)
	}
	asked := tags.BindingLists

	if _, err := p.PlanPreflight(ctx, preflightOf(environment.TierPreview)); err != nil {
		t.Fatalf("PlanPreflight() once the bootstrap bound every outside account = %v, want no refusal: the realtime and app accounts are the deploys' own", err)
	}
	if got := tags.BindingLists - asked; got != 0 {
		t.Errorf("the deploy read the tags of %d accounts, want none: the bootstrap recorded the ones it found tagged", got)
	}
}

func TestABootstrapAccountCarriesItsTiersTagBeforeItIsGrantedAnything(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	server := googletest.GrantedIAM()
	server.Accounts = map[string]bool{}
	c := server.Open(t)
	tags := googletest.NewTieredTags(t)
	tags.Tagged(c)
	const probe = "ocel-prod-probe"
	var boundWhenGranted []string
	b := bootstrap.Engine{Clients: c, Accounts: []bootstrap.Account{{
		Name: func(names.Names, environment.Tier) string { return probe },
		Purpose: func(bootstrap.Engine, environment.Tier) bootstrap.AccountPurpose {
			return bootstrap.AccountPurpose{Grant: func(context.Context, bootstrap.Engine, environment.Tier) error {
				boundWhenGranted = tags.Bound(googletest.AccountTagParent(probe))
				return nil
			}}
		},
	}}}

	if err := b.MakeAccount(ctx, bootstrap.Survey{Tier: environment.TierProduction, Names: c.Names}, probe); err != nil {
		t.Fatal(err)
	}
	if want := []string{tags.ValueOf(environment.TierProduction)}; !slices.Equal(boundWhenGranted, want) {
		t.Errorf("the account carried %v when its grants were made, want the production value already: an account granted before it is tagged can be bound to the other tier first", boundWhenGranted)
	}
}
