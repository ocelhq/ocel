package cloudflare

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func recordBootstrapAccountFor(t *testing.T, m *cfMock, tier environment.Tier, accountID string) {
	t.Helper()
	writeTierRecord(t, m.store(), tier, func(recorded *tierRecord) { recorded.AccountID = accountID })
}

func readBootstrapAccountOf(t *testing.T, m *cfMock, tier environment.Tier) string {
	t.Helper()
	return readTierRecordOf(t, m.store(), tier).AccountID
}

func boundStack(t *testing.T, p *cloudflare, tier environment.Tier) edge.EdgeStack {
	t.Helper()
	stack, err := p.Open(edge.StackState{
		Slug: "shop", Tier: tier, Endpoint: "https://store.invalid", Secret: "s3cr3t",
		Bound:   []string{"shop.app.com"},
		Private: edge.Own(private{EntryWorker: "ocel-shop"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return stack
}

func forwardedProxyStack(t *testing.T, x *Proxy, tier environment.Tier) edge.EdgeStack {
	t.Helper()
	stack, err := x.Open(edge.StackState{Slug: "shop", Tier: tier, Bound: []string{"shop.app.com"}})
	if err != nil {
		t.Fatal(err)
	}
	return stack
}

var shopOrigin = &edge.Origin{Address: "203.0.113.7"}

func tierOperations() map[string]func(context.Context, *testing.T, *cfMock) error {
	production, preview := environment.TierProduction, environment.TierPreview
	worker := func(t *testing.T, m *cfMock) *cloudflare { return m.provider(t) }
	proxy := func(t *testing.T, m *cfMock) *Proxy { return &Proxy{p: m.provider(t)} }
	return map[string]func(context.Context, *testing.T, *cfMock) error{
		"Bootstrap": func(ctx context.Context, t *testing.T, m *cfMock) error {
			_, err := worker(t, m).Bootstrap(ctx, production)
			return err
		},
		"PlanBootstrap": func(ctx context.Context, t *testing.T, m *cfMock) error {
			_, err := worker(t, m).Hooks().PlanBootstrap(ctx, production)
			return err
		},
		"DescribeBootstrap": func(ctx context.Context, t *testing.T, m *cfMock) error {
			_, err := worker(t, m).Hooks().DescribeBootstrap(ctx, production)
			return err
		},
		"PlanRemoveBootstrap": func(ctx context.Context, t *testing.T, m *cfMock) error {
			_, err := worker(t, m).Hooks().PlanRemoveBootstrap(ctx, production)
			return err
		},
		"Teardown": func(ctx context.Context, t *testing.T, m *cfMock) error {
			return worker(t, m).Teardown(ctx, production)
		},
		"Reconcile": func(ctx context.Context, t *testing.T, m *cfMock) error {
			_, err := worker(t, m).Reconcile(ctx, edge.StackSpec{Tier: production, Slug: "shop"}, edge.StackState{})
			return err
		},
		"BindDomain": func(ctx context.Context, t *testing.T, m *cfMock) error {
			return boundStack(t, worker(t, m), production).BindDomain(ctx, edge.DomainBinding{Hostname: "shop.app.com"})
		},
		"BindDomain forwarded": func(ctx context.Context, t *testing.T, m *cfMock) error {
			return boundStack(t, worker(t, m), production).BindDomain(ctx, edge.DomainBinding{Hostname: "shop.app.com", Origin: shopOrigin})
		},
		"UnbindDomain": func(ctx context.Context, t *testing.T, m *cfMock) error {
			return boundStack(t, worker(t, m), production).UnbindDomain(ctx, "shop.app.com")
		},
		"Destroy": func(ctx context.Context, t *testing.T, m *cfMock) error {
			return boundStack(t, worker(t, m), production).Destroy(ctx)
		},
		"PublishGate": func(ctx context.Context, t *testing.T, m *cfMock) error {
			state := boundStack(t, worker(t, m), production).State()
			return worker(t, m).Hooks().PublishGate(ctx, edge.GatePublication{State: state})
		},
		"ReconcilePreviewWildcard": func(ctx context.Context, t *testing.T, m *cfMock) error {
			_, err := worker(t, m).ReconcilePreviewWildcard(ctx, previewWildcardSpec())
			return err
		},
		"DestroyPreviewWildcard": func(ctx context.Context, t *testing.T, m *cfMock) error {
			return worker(t, m).DestroyPreviewWildcard(ctx, "preview.app.com")
		},
		"ClientCertificates.RefuseUnready": func(ctx context.Context, t *testing.T, m *cfMock) error {
			return worker(t, m).Hooks().ClientCertificates.RefuseUnready(ctx, production, "shop.app.com")
		},
		"ClientCertificates.Ensure": func(ctx context.Context, t *testing.T, m *cfMock) error {
			_, err := worker(t, m).Hooks().ClientCertificates.Ensure(ctx, production, "shop.app.com")
			return err
		},
		"ClientCertificates.Present": func(ctx context.Context, t *testing.T, m *cfMock) error {
			return worker(t, m).Hooks().ClientCertificates.Present(ctx, production, "shop.app.com")
		},
		"proxy Bootstrap": func(ctx context.Context, t *testing.T, m *cfMock) error {
			_, err := proxy(t, m).Bootstrap(ctx, production)
			return err
		},
		"proxy PlanBootstrap": func(ctx context.Context, t *testing.T, m *cfMock) error {
			_, err := proxy(t, m).Hooks().PlanBootstrap(ctx, production)
			return err
		},
		"proxy PlanRemoveBootstrap": func(ctx context.Context, t *testing.T, m *cfMock) error {
			_, err := proxy(t, m).Hooks().PlanRemoveBootstrap(ctx, production)
			return err
		},
		"proxy Teardown": func(ctx context.Context, t *testing.T, m *cfMock) error {
			return proxy(t, m).Teardown(ctx, production)
		},
		"proxy Reconcile": func(ctx context.Context, t *testing.T, m *cfMock) error {
			_, err := proxy(t, m).Reconcile(ctx, proxySpec(), edge.StackState{})
			return err
		},
		"proxy BindDomain": func(ctx context.Context, t *testing.T, m *cfMock) error {
			return forwardedProxyStack(t, proxy(t, m), production).BindDomain(ctx, edge.DomainBinding{Hostname: "shop.app.com", Origin: shopOrigin})
		},
		"proxy UnbindDomain": func(ctx context.Context, t *testing.T, m *cfMock) error {
			return forwardedProxyStack(t, proxy(t, m), production).UnbindDomain(ctx, "shop.app.com")
		},
		"proxy Destroy": func(ctx context.Context, t *testing.T, m *cfMock) error {
			return forwardedProxyStack(t, proxy(t, m), preview).Destroy(ctx)
		},
		"proxy ReconcilePreviewWildcard": func(ctx context.Context, t *testing.T, m *cfMock) error {
			_, err := proxy(t, m).ReconcilePreviewWildcard(ctx, edge.PreviewWildcardSpec{BaseDomain: "preview.app.com", Origin: shopOrigin})
			return err
		},
		"proxy DestroyPreviewWildcard": func(ctx context.Context, t *testing.T, m *cfMock) error {
			return proxy(t, m).DestroyPreviewWildcard(ctx, "preview.app.com")
		},
		"DomainOwner": func(ctx context.Context, t *testing.T, m *cfMock) error {
			_, err := worker(t, m).DomainOwner(ctx, "shop.app.com")
			return err
		},
		"proxy DomainOwner": func(ctx context.Context, t *testing.T, m *cfMock) error {
			_, err := proxy(t, m).DomainOwner(ctx, "shop.app.com")
			return err
		},
		"PurgeHostnames": func(ctx context.Context, t *testing.T, m *cfMock) error {
			return proxy(t, m).Hooks().PurgeHostnames(ctx, []string{"shop.app.com"})
		},
		"OriginCertificates.Issue": func(ctx context.Context, t *testing.T, m *cfMock) error {
			_, err := proxy(t, m).Hooks().OriginCertificates.Issue(ctx, "shop.app.com")
			return err
		},
		"OriginCertificates.Revoke": func(ctx context.Context, t *testing.T, m *cfMock) error {
			return proxy(t, m).Hooks().OriginCertificates.Revoke(ctx, "cert-1")
		},
		"Tunnels.Ensure": func(ctx context.Context, t *testing.T, m *cfMock) error {
			_, err := proxy(t, m).Hooks().Tunnels.Ensure(ctx, "ocel-box")
			return err
		},
		"Tunnels.Configure": func(ctx context.Context, t *testing.T, m *cfMock) error {
			return proxy(t, m).Hooks().Tunnels.Configure(ctx, "tunnel-1", "http://localhost:80")
		},
		"Tunnels.ReadToken": func(ctx context.Context, t *testing.T, m *cfMock) error {
			_, err := proxy(t, m).Hooks().Tunnels.ReadToken(ctx, "tunnel-1")
			return err
		},
		"Tunnels.Delete": func(ctx context.Context, t *testing.T, m *cfMock) error {
			return proxy(t, m).Hooks().Tunnels.Delete(ctx, "tunnel-1")
		},
	}
}

func TestEveryOperationOnATierBootstrappedAgainstAnotherAccountIsRefusedBeforeCloudflareIsAsked(t *testing.T) {
	for name, operate := range tierOperations() {
		t.Run(name, func(t *testing.T) {
			seedBootstrapBundle(t, "export default {}")
			t.Setenv(envAccountID, "acct-second")
			t.Setenv(envAPIToken, "tok")
			m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
			recordBootstrapAccountFor(t, m, environment.TierProduction, "acct-first")
			recordBootstrapAccountFor(t, m, environment.TierPreview, "acct-first")

			err := operate(t.Context(), t, m)

			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
				t.Fatalf("err = %v, want a refusal (%s) over the account the tier was bootstrapped against", err, refusal.CodeInvalid)
			}
			for _, named := range []string{"acct-first", "acct-second", envAccountID, "ocel bootstrap destroy"} {
				if !strings.Contains(err.Error(), named) {
					t.Errorf("refusal %q does not name %s, so the user cannot tell which account holds the tier or how to move it", err, named)
				}
			}
			if m.requests != 0 {
				t.Errorf("Cloudflare was sent %d requests, want none: the other account holds nothing of this tier's", m.requests)
			}
		})
	}
}

func TestBootstrapRecordsTheAccountItRanAgainst(t *testing.T) {
	seedBootstrapBundle(t, "export default {}")
	m := bootstrapMock(t)

	if _, err := m.provider(t).Bootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if got := readBootstrapAccountOf(t, m, environment.TierProduction); got != "acct" {
		t.Errorf("recorded account = %q, want acct", got)
	}
	if got := readBootstrapAccountOf(t, m, environment.TierPreview); got != "" {
		t.Errorf("preview recorded account = %q, want none: only production was bootstrapped", got)
	}
}

func TestTheProxyBootstrapRecordsTheAccountItRanAgainst(t *testing.T) {
	m := bootstrapMock(t)

	if _, err := (&Proxy{p: m.provider(t)}).Bootstrap(t.Context(), environment.TierPreview); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if got := readBootstrapAccountOf(t, m, environment.TierPreview); got != "acct" {
		t.Errorf("recorded account = %q, want acct", got)
	}
	if m.requests != 0 {
		t.Errorf("Cloudflare was sent %d requests, want none: the proxy keeps nothing per tier until a hostname is forwarded", m.requests)
	}
}

func TestBootstrapAgainstTheRecordedAccountProceeds(t *testing.T) {
	seedBootstrapBundle(t, "export default {}")
	m := bootstrapMock(t)
	recordBootstrapAccountFor(t, m, environment.TierProduction, "acct")

	if _, err := m.provider(t).Bootstrap(t.Context(), environment.TierProduction); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if len(m.putScripts) == 0 {
		t.Error("Bootstrap uploaded nothing, want the releases store put in the account the tier was bootstrapped against")
	}
}

func TestBootstrapRacingAnotherAccountsBootstrapIsRefusedBeforeCloudflareIsAsked(t *testing.T) {
	seedBootstrapBundle(t, "export default {}")
	m := bootstrapMock(t)
	m.store().BeforeNextWrite(tierRecordKey(environment.TierProduction), func() {
		recordBootstrapAccountFor(t, m, environment.TierProduction, "acct-first")
	})

	_, err := m.provider(t).Bootstrap(t.Context(), environment.TierProduction)

	if err == nil || !strings.Contains(err.Error(), "acct-first") {
		t.Fatalf("Bootstrap err = %v, want a refusal naming acct-first, whose bootstrap recorded the tier first", err)
	}
	if m.requests != 0 {
		t.Errorf("Cloudflare was sent %d requests, want none", m.requests)
	}
}

func TestTeardownForgetsTheRecordedAccountLast(t *testing.T) {
	for name, teardown := range tierTeardowns() {
		t.Run(name, func(t *testing.T) {
			m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
			recordBootstrapAccountFor(t, m, environment.TierProduction, "acct")

			if err := teardown(t, m); err != nil {
				t.Fatalf("Teardown: %v", err)
			}
			if got := readBootstrapAccountOf(t, m, environment.TierProduction); got != "" {
				t.Errorf("recorded account = %q after teardown, want it forgotten so the tier can be bootstrapped against any account", got)
			}
		})
	}

	t.Run("a teardown that fails keeps the account", func(t *testing.T) {
		t.Setenv(envAccountID, "acct")
		m := &cfMock{zoneID: "zone1", zoneName: "app.com", refuseScriptDeletes: map[string]bool{sharedStoreScriptName: true}}
		recordBootstrapAccountFor(t, m, environment.TierProduction, "acct")

		if err := m.provider(t).Teardown(t.Context(), environment.TierProduction); err == nil {
			t.Fatal("Teardown err = nil, want the refused delete reported")
		}
		if got := readBootstrapAccountOf(t, m, environment.TierProduction); got != "acct" {
			t.Errorf("recorded account = %q, want acct kept: the releases store still lives there", got)
		}
	})
}
