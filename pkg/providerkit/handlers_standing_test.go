package providerkit_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

type standingProvider struct {
	*fake.Provider
	asked   []providerkit.StandingRequest
	answer  []providerkit.StandingCheck
	refusal error
}

func (s *standingProvider) CheckStanding(_ context.Context, req providerkit.StandingRequest) ([]providerkit.StandingCheck, error) {
	s.asked = append(s.asked, req)
	return s.answer, s.refusal
}

func standingServed(t *testing.T, answer []providerkit.StandingCheck, refusal error) (*standingProvider, *contractv1.PreflightResponse) {
	t.Helper()
	provider := &standingProvider{
		Provider: fake.NewProvider(fake.Options{Region: "nowhere"}),
		answer:   answer,
		refusal:  refusal,
	}
	client := servedProvider(t, "1.2.3", provider)
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})

	resp, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
		Domains:      []string{"shop.example.com", "www.example.com"},
	})
	if refusal != nil {
		if err == nil {
			t.Fatalf("Preflight() = %+v, want the refusal the standing port answered with", resp)
		}
		return provider, nil
	}
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	return provider, resp
}

func TestPreflightHandsTheStandingPortEveryHostnameItWasAskedAbout(t *testing.T) {
	t.Parallel()

	provider, resp := standingServed(t, []providerkit.StandingCheck{
		{Subject: "shop.example.com", Verdict: providerkit.StandingPass, Finding: "points here"},
	}, nil)

	if len(provider.asked) != 1 {
		t.Fatalf("the standing port was asked %d times, want once per preflight", len(provider.asked))
	}
	asked := provider.asked[0]
	if asked.Class != providerkit.ClassProduction {
		t.Errorf("the standing port was asked about %s, want %s", asked.Class, providerkit.ClassProduction)
	}
	if want := []string{"shop.example.com", "www.example.com"}; !slices.Equal(asked.Hostnames, want) {
		t.Errorf("the standing port was handed %v, want %v: the handler used to discard the requested hostnames", asked.Hostnames, want)
	}
	if len(resp.GetStanding()) != 1 {
		t.Fatalf("Preflight() carried %d standing checks, want the one the provider answered", len(resp.GetStanding()))
	}
}

func TestPreflightCarriesEveryStandingVerdictAndRefusesOnNone(t *testing.T) {
	t.Parallel()

	_, resp := standingServed(t, []providerkit.StandingCheck{
		{Subject: "shop.example.com", Verdict: providerkit.StandingPass, Finding: "resolves to this box"},
		{Subject: "www.example.com", Verdict: providerkit.StandingOwed, Finding: "does not resolve yet", Fix: "add the record"},
		{Subject: "", Verdict: providerkit.StandingFail, Finding: "something listens on 2019", Fix: "rebootstrap"},
	}, nil)

	carried := resp.GetStanding()
	if len(carried) != 3 {
		t.Fatalf("Preflight() carried %d standing checks, want 3", len(carried))
	}
	wants := []contractv1.StandingCheck_Verdict{
		contractv1.StandingCheck_VERDICT_PASS,
		contractv1.StandingCheck_VERDICT_OWED,
		contractv1.StandingCheck_VERDICT_FAIL,
	}
	for i, want := range wants {
		if carried[i].GetVerdict() != want {
			t.Errorf("standing check %d is %v, want %v", i, carried[i].GetVerdict(), want)
		}
		if carried[i].GetFinding() == "" {
			t.Errorf("standing check %d carries no finding, and a verdict nobody can read is not one", i)
		}
	}
	if carried[1].GetFix() != "add the record" || carried[2].GetFix() != "rebootstrap" {
		t.Errorf("standing checks lost their fixes: %+v", carried)
	}
	if !resp.GetInfrastructurePresent() {
		t.Error("a failing standing check took the preflight's verdict with it, and a standing check is a report and never a gate")
	}
}

func TestPreflightCarriesNoStandingChecksFromAProviderThatAnswersNone(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})

	resp, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
		Domains:      []string{"shop.example.com"},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if !resp.GetBootstrap().GetPresent() {
		t.Fatal("this preflight answered nothing about the bootstrap it just stood up, so the response is not the window an absence can be read over")
	}
	if len(resp.GetStanding()) != 0 {
		t.Errorf("Preflight() carried %+v, want nothing from a provider that answers no standing port", resp.GetStanding())
	}
}

func TestPreflightSurfacesAStandingPortThatCouldNotAnswer(t *testing.T) {
	t.Parallel()

	standingServed(t, nil, errors.New("the machine did not answer"))
}
