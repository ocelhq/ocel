package vps

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
)

var consoleTrusted = provider.WebIdentityTrust{
	Issuer:         "https://console.example.com",
	Audience:       "ocel-target:target-1",
	OrganizationID: "org-1",
	TargetID:       "target-1",
}

type boxTrust struct {
	trusts       map[environment.Tier]provider.WebIdentityTrust
	organization string
	removed      []environment.Tier
}

func (b *boxTrust) Read(_ context.Context, tier environment.Tier) (host.TrustReading, error) {
	reading := host.TrustReading{Observed: map[string]string{}, ConnectorOrganization: b.organization}
	if sibling, ok := b.trusts[tier.Sibling()]; ok {
		reading.Sibling = &sibling
	}
	trust, ok := b.trusts[tier]
	if !ok {
		return reading, nil
	}
	reading.Trust, reading.Observed = &trust, machine.DigestsOf(host.ConsoleTrustItems(tier, trust))
	return reading, nil
}

func (b *boxTrust) Apply(_ context.Context, tier environment.Tier, trust provider.WebIdentityTrust, _ host.TrustReading, _ progress.Log) error {
	b.trusts[tier] = trust
	return nil
}

func (b *boxTrust) Remove(_ context.Context, tier environment.Tier, _ progress.Log) error {
	delete(b.trusts, tier)
	b.removed = append(b.removed, tier)
	return nil
}

func refusedWith(err error, code refusal.Code) bool {
	var refused refusal.Refusal
	return errors.As(err, &refused) && refused.Code == code
}

func trustingOn(hostname string) (consoleTrustPart, *fake.Bootstrap, *boxTrust) {
	inner := fake.NewBootstrap()
	trust := &boxTrust{trusts: map[environment.Tier]provider.WebIdentityTrust{}}
	return consoleTrustPart{Bootstrap: inner, trust: trust, hostname: func(context.Context) (string, error) { return hostname, nil }}, inner, trust
}

func TestTheVPSBootstrapOffersTheConsolesTrust(t *testing.T) {
	bootstrap, _, _ := trustingOn("box.example.com")
	if !slices.ContainsFunc(bootstrap.Parts(), func(part provider.Part) bool { return part.Name == provider.ConsoleTrustPart }) {
		t.Errorf("parts = %+v, want %s", bootstrap.Parts(), provider.ConsoleTrustPart)
	}
}

func TestABootstrapHandedTheConsolesTrustNamesTheBoxsEndpointAndNoPrincipal(t *testing.T) {
	ctx := context.Background()
	bootstrap, inner, _ := trustingOn("box.example.com")
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{provider.ConsoleTrustPart}, Trust: &consoleTrusted}, progress.Discard()); err != nil {
		t.Fatal(err)
	}
	for _, applied := range inner.Applied() {
		if slices.Contains(applied.Parts, provider.ConsoleTrustPart) || applied.Trust != nil {
			t.Errorf("the box's own bootstrap was handed %+v, want the console's trust kept to the trust part", applied)
		}
	}

	described, err := bootstrap.Describe(ctx, environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	want := provider.TierIdentity{IdentityProvider: "https://box.example.com" + boxapi.ConnectorPath}
	if described.Identity == nil || *described.Identity != want {
		t.Errorf("identity = %+v, want %+v", described.Identity, want)
	}
	if described.Trust == nil || *described.Trust != consoleTrusted {
		t.Errorf("trust = %+v, want %+v", described.Trust, consoleTrusted)
	}
	current := slices.ContainsFunc(described.Stacks, func(stack provider.BootstrapStack) bool {
		return stack.Part == provider.ConsoleTrustPart && stack.Present && stack.DigestCurrent
	})
	if !current {
		t.Errorf("stacks = %+v, want the trust part present and current, so a deploy with a credential passes preflight", described.Stacks)
	}
}

func TestABoxReachedAtAnAddressIsNotRegisteredWithTheConsole(t *testing.T) {
	bootstrap, _, _ := trustingOn("203.0.113.10")
	_, err := bootstrap.Plan(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{provider.ConsoleTrustPart}, Trust: &consoleTrusted})
	if !refusedWith(err, refusal.CodeNotReady) {
		t.Fatalf("err = %v, want the address refused for a hostname", err)
	}
}

func TestAPlanToTrustTheConsoleShowsTheTrustPartsItems(t *testing.T) {
	bootstrap, _, _ := trustingOn("box.example.com")
	plan, err := bootstrap.Plan(context.Background(), provider.BootstrapRequest{Tier: environment.TierPreview, Trust: &consoleTrusted})
	if err != nil {
		t.Fatal(err)
	}
	at := slices.IndexFunc(plan.Groups, func(group provider.ChangeGroup) bool { return group.Part == provider.ConsoleTrustPart })
	if at < 0 {
		t.Fatalf("groups = %+v, want one for %s", plan.Groups, provider.ConsoleTrustPart)
	}
	if group := plan.Groups[at]; group.Action != provider.ActionCreate || len(group.Changes) != len(host.ConsoleTrustItems(environment.TierPreview, consoleTrusted)) {
		t.Errorf("group = %+v, want every item created", group)
	}
}

func TestReapplyingTheTrustPartKeepsTheConsoleItRecorded(t *testing.T) {
	ctx := context.Background()
	bootstrap, _, trust := trustingOn("box.example.com")
	trust.trusts[environment.TierProduction] = consoleTrusted
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{provider.ConsoleTrustPart}}, progress.Discard()); err != nil {
		t.Fatal(err)
	}
	if trust.trusts[environment.TierProduction] != consoleTrusted {
		t.Errorf("trust = %+v, want the recorded one kept", trust.trusts[environment.TierProduction])
	}
}

func TestTheTrustPartWithNoConsoleRecordedOrHandedIsRefused(t *testing.T) {
	bootstrap, _, _ := trustingOn("box.example.com")
	err := bootstrap.Apply(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{provider.ConsoleTrustPart}}, progress.Discard())
	if !refusedWith(err, refusal.CodeInvalid) {
		t.Fatalf("err = %v, want a refusal naming the login", err)
	}
}

func TestRemovingTheTrustPartForgetsTheConsole(t *testing.T) {
	ctx := context.Background()
	bootstrap, _, trust := trustingOn("box.example.com")
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: environment.TierPreview, Trust: &consoleTrusted}, progress.Discard()); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: environment.TierPreview, Remove: []string{provider.ConsoleTrustPart}}, progress.Discard()); err != nil {
		t.Fatal(err)
	}
	described, err := bootstrap.Describe(ctx, environment.TierPreview)
	if err != nil {
		t.Fatal(err)
	}
	if described.Identity != nil || described.Trust != nil || !slices.Equal(trust.removed, []environment.Tier{environment.TierPreview}) {
		t.Errorf("identity %+v, trust %+v, removed %v after removing the part, want none and preview removed", described.Identity, described.Trust, trust.removed)
	}
}

func TestATrustTheBoxsConnectorWouldRefuseIsRefusedBeforeItIsWritten(t *testing.T) {
	ctx := context.Background()
	other := consoleTrusted
	other.TargetID, other.Audience = "target-2", "ocel-target:target-2"

	bootstrap, _, trust := trustingOn("box.example.com")
	trust.trusts[environment.TierProduction] = other
	if _, err := bootstrap.Plan(ctx, provider.BootstrapRequest{Tier: environment.TierPreview, Trust: &consoleTrusted}); !refusedWith(err, refusal.CodeInvalid) {
		t.Errorf("plan with production trusting another target = %v, want refused", err)
	}

	bootstrap, _, trust = trustingOn("box.example.com")
	trust.organization = "org-2"
	err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: environment.TierPreview, Trust: &consoleTrusted}, progress.Discard())
	if !refusedWith(err, refusal.CodeInvalid) {
		t.Errorf("apply with a connector of another organization = %v, want refused", err)
	}
	if _, written := trust.trusts[environment.TierPreview]; written {
		t.Error("the trust was written anyway")
	}
}

func TestPreviewOnABoxThatServesProductionGetsNoConsoleTrust(t *testing.T) {
	ctx := context.Background()
	bootstrap, inner, trust := trustingOn("box.example.com")
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: environment.TierProduction}, progress.Discard()); err != nil {
		t.Fatal(err)
	}
	applied := len(inner.Applied())

	preview := provider.BootstrapRequest{Tier: environment.TierPreview, Trust: &consoleTrusted}
	_, err := bootstrap.Plan(ctx, preview)
	if !refusedWith(err, refusal.CodeInvalid) || !strings.Contains(err.Error(), "box of their own") || !strings.Contains(err.Error(), "your own ssh") {
		t.Errorf("plan = %v, want the shared login named with both ways forward", err)
	}
	if err := bootstrap.Apply(ctx, preview, progress.Discard()); !refusedWith(err, refusal.CodeInvalid) {
		t.Errorf("apply = %v, want refused", err)
	}
	if _, written := trust.trusts[environment.TierPreview]; written || len(inner.Applied()) != applied {
		t.Error("the refused bootstrap wrote something")
	}

	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: environment.TierPreview}, progress.Discard()); err != nil {
		t.Errorf("a preview bootstrap with no console trust on the shared box = %v, want it allowed", err)
	}
}

func TestProductionOnABoxWhosePreviewTakesConsoleCredentialsIsRefusedUntilThatTrustGoes(t *testing.T) {
	ctx := context.Background()
	bootstrap, inner, trust := trustingOn("box.example.com")
	trust.trusts[environment.TierPreview] = consoleTrusted

	_, err := bootstrap.Plan(ctx, provider.BootstrapRequest{Tier: environment.TierProduction})
	if !refusedWith(err, refusal.CodeInvalid) || !strings.Contains(err.Error(), "ocel bootstrap preview --remove console-trust") {
		t.Errorf("plan = %v, want the preview trust's removal named", err)
	}
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: environment.TierProduction, Trust: &consoleTrusted}, progress.Discard()); !refusedWith(err, refusal.CodeInvalid) || len(inner.Applied()) != 0 {
		t.Errorf("apply = %v after %d applies, want refused before anything is written", err, len(inner.Applied()))
	}

	delete(trust.trusts, environment.TierPreview)
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: environment.TierProduction, Trust: &consoleTrusted}, progress.Discard()); err != nil {
		t.Errorf("production console trust on a box whose preview takes none = %v, want it allowed", err)
	}
}
