package providerkit_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type checkingProvider struct {
	*fake.Provider
	asked   []provider.HostCheckRequest
	answer  []provider.HostCheck
	refusal error
}

func (s *checkingProvider) Hooks() provider.Hooks {
	hooks := s.Provider.Hooks()
	hooks.CheckHost = s.CheckHost
	return hooks
}

func (s *checkingProvider) CheckHost(_ context.Context, req provider.HostCheckRequest) ([]provider.HostCheck, error) {
	s.asked = append(s.asked, req)
	return s.answer, s.refusal
}

func hostChecksServed(t *testing.T, answer []provider.HostCheck, refusal error) (*checkingProvider, *contractv1.PreflightResponse) {
	t.Helper()
	p := &checkingProvider{
		Provider: fake.NewProvider(fake.Options{Region: "nowhere"}),
		answer:   answer,
		refusal:  refusal,
	}
	client := servedProvider(t, "1.2.3", p)
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})

	resp, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier:     environmentv1.Tier_TIER_PRODUCTION,
		Slug:             "shop",
		Domains:          []string{"shop.example.com"},
		CheckHosts:       true,
		HostCheckDomains: []string{"shop.example.com", "www.example.com"},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	return p, resp
}

func TestTheHostCheckIsAskedAboutTheNamesTheCallerWillRenderRatherThanThisTiers(t *testing.T) {
	t.Parallel()

	p, _ := hostChecksServed(t, []provider.HostCheck{
		{Subject: "shop.example.com", Verdict: provider.HostPass, Finding: "points here"},
	}, nil)

	if len(p.asked) != 1 {
		t.Fatalf("the host check was asked %d times, want once per preflight", len(p.asked))
	}
	if want := []string{"shop.example.com", "www.example.com"}; !slices.Equal(p.asked[0].Hostnames, want) {
		t.Errorf("the host check was handed %v, want %v: what stands is one box's business and the caller renders one section over every tier, so asking per tier buys a dns round, a dial and an exec for each of them",
			p.asked[0].Hostnames, want)
	}
}

func TestAPreflightThatWillRenderNoHostCheckSectionAsksTheBoxForNone(t *testing.T) {
	t.Parallel()

	p := &checkingProvider{
		Provider: fake.NewProvider(fake.Options{Region: "nowhere"}),
		answer: []provider.HostCheck{
			{Subject: "shop.example.com", Verdict: provider.HostPass, Finding: "points here"},
		},
	}
	client := servedProvider(t, "1.2.3", p)
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})

	resp, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
		Domains:      []string{"shop.example.com", "www.example.com"},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if !resp.GetInfrastructurePresent() {
		t.Fatal("this preflight answered nothing about the bootstrap it just stood up, so the response is not the window an absence can be read over")
	}
	if len(p.asked) != 0 {
		t.Errorf("the host check was asked %d times by a caller that renders none of it: every `ocel deploy` calls this rpc, and it pays for a dns round per hostname, a dial to the renewal port and an exec into the proxy for an answer it throws away",
			len(p.asked))
	}
	if len(resp.GetHostChecks()) != 0 {
		t.Errorf("Preflight() carried %+v to a caller that asked for none of it", resp.GetHostChecks())
	}
}

func TestPreflightHandsTheStandingPortEveryHostnameItWasAskedAbout(t *testing.T) {
	t.Parallel()

	p, resp := hostChecksServed(t, []provider.HostCheck{
		{Subject: "shop.example.com", Verdict: provider.HostPass, Finding: "points here"},
	}, nil)

	if len(p.asked) != 1 {
		t.Fatalf("the host check was asked %d times, want once per preflight", len(p.asked))
	}
	asked := p.asked[0]
	if asked.Class != edge.ClassProduction {
		t.Errorf("the host check was asked about %s, want %s", asked.Class, edge.ClassProduction)
	}
	if want := []string{"shop.example.com", "www.example.com"}; !slices.Equal(asked.Hostnames, want) {
		t.Errorf("the host check was handed %v, want %v: the handler used to discard the requested hostnames", asked.Hostnames, want)
	}
	if len(resp.GetHostChecks()) != 1 {
		t.Fatalf("Preflight() carried %d host checks, want the one the provider answered", len(resp.GetHostChecks()))
	}
}

func TestPreflightCarriesEveryStandingVerdictAndRefusesOnNone(t *testing.T) {
	t.Parallel()

	_, resp := hostChecksServed(t, []provider.HostCheck{
		{Subject: "shop.example.com", Verdict: provider.HostPass, Finding: "resolves to this box"},
		{Subject: "www.example.com", Verdict: provider.HostOwed, Finding: "does not resolve yet", Fix: "add the record"},
		{Subject: "", Verdict: provider.HostFail, Finding: "something listens on 2019", Fix: "rebootstrap"},
	}, nil)

	carried := resp.GetHostChecks()
	if len(carried) != 3 {
		t.Fatalf("Preflight() carried %d host checks, want 3", len(carried))
	}
	wants := []contractv1.HostCheck_Verdict{
		contractv1.HostCheck_VERDICT_PASS,
		contractv1.HostCheck_VERDICT_OWED,
		contractv1.HostCheck_VERDICT_FAIL,
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
		t.Errorf("host checks lost their fixes: %+v", carried)
	}
	if !resp.GetInfrastructurePresent() {
		t.Error("a failing standing check took the preflight's verdict with it, and a standing check is a report and never a gate")
	}
}

func TestPreflightCarriesNoHostChecksFromAProviderThatAnswersNone(t *testing.T) {
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
	if len(resp.GetHostChecks()) != 0 {
		t.Errorf("Preflight() carried %+v, want nothing from a provider that answers no host check", resp.GetHostChecks())
	}
}

func TestAStandingPortThatCouldNotAnswerIsReportedAndNeverGates(t *testing.T) {
	t.Parallel()

	_, resp := hostChecksServed(t, nil, errors.New("the machine did not answer"))

	if !resp.GetInfrastructurePresent() {
		t.Fatal("a standing port that could not answer took the preflight's verdict with it, and every `ocel deploy` calls this same rpc: a standing concern is a report and never a gate")
	}
	carried := resp.GetHostChecks()
	if len(carried) != 1 {
		t.Fatalf("Preflight() carried %d host checks over a port that could not answer, want the one that says so", len(carried))
	}
	if carried[0].GetVerdict() != contractv1.HostCheck_VERDICT_FAIL {
		t.Errorf("verdict = %v, want a failure: a check nothing could be read for is not a check that passed", carried[0].GetVerdict())
	}
	if !strings.Contains(carried[0].GetFinding(), "the machine did not answer") {
		t.Errorf("finding = %q, want what the host check said carried into the report rather than swallowed", carried[0].GetFinding())
	}
}

func TestADeployProceedsAgainstABoxWhoseStandingFailed(t *testing.T) {
	builtProject(t)

	p := &checkingProvider{
		Provider: fake.NewProvider(fake.Options{Region: "nowhere"}),
		answer: []provider.HostCheck{
			{Subject: "shop.example.com", Verdict: provider.HostFail, Finding: "points somewhere else", Fix: "move the record"},
		},
	}
	client := servedProvider(t, "1.2.3", p)
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})

	resp, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
		Domains:      []string{"shop.example.com"},
		CheckHosts:   true,
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if len(resp.GetHostChecks()) != 1 || resp.GetHostChecks()[0].GetVerdict() != contractv1.HostCheck_VERDICT_FAIL {
		t.Fatalf("Preflight() carried %+v, and this deploy is not running against the failing verdict the test is about", resp.GetHostChecks())
	}

	result, _, err := deployStream(t, client, deployRequest())
	if err != nil {
		t.Fatalf("Deploy() = %v against a box whose standing verdict failed, want it to proceed: a standing check reports and never gates", err)
	}
	if !result.GetSuccess() {
		t.Fatalf("Deploy() reported %q against a box whose standing verdict failed, want it to proceed: a standing check reports and never gates", result.GetError())
	}
}
