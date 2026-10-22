package vps_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func TestABoxRunsTheTunnelTheCloudflareProxyReachesItThrough(t *testing.T) {
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (ssh.Conn, error) {
			return nil, errors.New("no box is reached to read what it runs")
		},
	)
	front, err := p.Edges().Open(cloudflare.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}

	if tunnels := front.Hooks().Tunnels; !p.Facts().RunsTunnels || tunnels == nil || tunnels.Connector.Image == "" {
		t.Errorf("Facts().RunsTunnels = %v with the Cloudflare proxy's tunnels %+v, want a box reachable through the tunnel the proxy opens, running the connector it names",
			p.Facts().RunsTunnels, tunnels)
	}
}

func TestABoxRefusesATunnelToAnEdgeThatOpensNone(t *testing.T) {
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (ssh.Conn, error) { return nil, errors.New("no box is reached to open an edge") },
	)

	_, err := p.FindTunnelHooks(edge.None)

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(err.Error(), "Remove `tunnel`") {
		t.Errorf("FindTunnelHooks(none) = %v, want it refused invalid, naming the option to remove: an edge that opens no tunnel names no connector for the box to run", err)
	}
}

func TestTheCloudflareProxyOpenedWithTunnelSetReachesTheBoxThroughATunnel(t *testing.T) {
	t.Setenv(provider.NamespaceEnvVar, "staging")
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (ssh.Conn, error) { return nil, errors.New("no box is reached to open an edge") },
	)

	tunneled, err := p.Edges().Open(cloudflare.Kind, provider.Options{"tunnel": true})
	if err != nil {
		t.Fatal(err)
	}
	addressed, err := p.Edges().Open(cloudflare.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}

	if !tunneled.Facts().TunnelsToOrigin || addressed.Facts().TunnelsToOrigin {
		t.Errorf("the proxy tunnels to the box %v with tunnel set and %v without, want only the one set to", tunneled.Facts().TunnelsToOrigin, addressed.Facts().TunnelsToOrigin)
	}
}

func TestTheCloudflareProxyInFrontOfABoxOwnsWhatItForwardsInOcelsNamespace(t *testing.T) {
	t.Setenv(provider.NamespaceEnvVar, "staging")
	machine := &box{}
	written, err := routing.WriteRoutingTable(boxapi.RoutingTable{Grace: routing.DrainWindow, OriginID: "k3j9x0"})
	if err != nil {
		t.Fatal(err)
	}
	machine.routingDoc = string(written)
	p := over(machine)

	front, err := p.Edges().Open(cloudflare.Kind, nil)
	if err != nil {
		t.Fatalf("Edges().Open(%q): %v", cloudflare.Kind, err)
	}
	got, err := front.ProjectOwner(t.Context(), "shop", environment.TierProduction)
	if err != nil {
		t.Fatalf("ProjectOwner: %v", err)
	}
	want, err := cloudflare.NewProxy("staging", cloudflare.Origin{Scope: func(context.Context) (cloudflare.OriginScope, error) {
		return cloudflare.OriginScope{Vendor: "vps", ID: "k3j9x0"}, nil
	}}, cloudflare.Options{}).ProjectOwner(t.Context(), "shop", environment.TierProduction)
	if err != nil {
		t.Fatalf("ProjectOwner: %v", err)
	}
	if got != want {
		t.Errorf("the proxy in front of a box owns shop's records as %q, want %q: it records ownership in the namespace ocel runs in, as it does in front of Google Cloud, not under the box's ssh destination", got, want)
	}
}

func TestABoxWhoseBootstrapNeverMintedAnOriginIDRemovesItsCloudflareProxyWithNothingToDo(t *testing.T) {
	for name, accountID := range map[string]string{"with CLOUDFLARE_ACCOUNT_ID set": "acct", "with CLOUDFLARE_ACCOUNT_ID unset": ""} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(provider.NamespaceEnvVar, "staging")
			t.Setenv("CLOUDFLARE_ACCOUNT_ID", accountID)
			t.Setenv("CLOUDFLARE_API_TOKEN", "")
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("%s %s reached Cloudflare, want nothing asked of an account this box's proxy never bootstrapped against", r.Method, r.URL.Path)
				http.Error(w, `{"success":false,"errors":[{"code":1,"message":"not served"}]}`, http.StatusInternalServerError)
			}))
			t.Cleanup(api.Close)
			t.Setenv("CLOUDFLARE_BASE_URL", api.URL+"/")
			machine := &box{}
			written, err := routing.WriteRoutingTable(boxapi.RoutingTable{Grace: routing.DrainWindow})
			if err != nil {
				t.Fatal(err)
			}
			machine.routingDoc = string(written)
			p := over(machine)
			p.Recording(fake.NewKeyValues())
			front, err := p.Edges().Open(cloudflare.Kind, nil)
			if err != nil {
				t.Fatal(err)
			}

			planned, err := front.Hooks().PlanRemoveBootstrap(t.Context(), environment.TierProduction)
			if err != nil || len(planned) != 0 {
				t.Errorf("PlanRemoveBootstrap() = %+v, %v, want nothing planned: a box whose first bootstrap failed before minting its origin id never bootstrapped the proxy", planned, err)
			}
			if err := front.Teardown(t.Context(), environment.TierProduction); err != nil {
				t.Errorf("Teardown() = %v, want nothing to tear down, so `ocel bootstrap destroy` reaches the box", err)
			}
		})
	}
}

func cloudflareZones(t *testing.T) {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/zones") {
			http.Error(w, `{"success":false,"errors":[{"code":1,"message":"not served"}]}`, http.StatusInternalServerError)
			return
		}
		result := `[{"id":"zone-example","name":"example.com"}]`
		if page := r.URL.Query().Get("page"); page != "" && page != "1" {
			result = `[]`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":` + result + `,"result_info":{"page":1,"per_page":20,"count":1,"total_count":1,"total_pages":1}}`))
	}))
	t.Cleanup(api.Close)
	t.Setenv("CLOUDFLARE_BASE_URL", api.URL+"/")
}

func TestReadingWhatTheCloudflareProxyOwnsNeverWritesTheBoxsRoutingTable(t *testing.T) {
	forwarded := edge.Record{Name: "shop.example.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}
	for name, read := range map[string]func(context.Context, *vps.Provider) error{
		"ProjectOwner": func(ctx context.Context, p *vps.Provider) error {
			front, err := p.Edges().Open(cloudflare.Kind, nil)
			if err != nil {
				return err
			}
			_, err = front.ProjectOwner(ctx, "shop", environment.TierProduction)
			return err
		},
		"DomainOwner": func(ctx context.Context, p *vps.Provider) error {
			front, err := p.Edges().Open(cloudflare.Kind, nil)
			if err != nil {
				return err
			}
			_, err = front.DomainOwner(ctx, forwarded.Name)
			return err
		},
		"DNS Delete": func(ctx context.Context, p *vps.Provider) error {
			records, err := p.DNS().Open(provider.DNSKind(cloudflare.Kind), "", cloudflare.Kind)
			if err != nil {
				return err
			}
			return records.Delete(ctx, []edge.Record{forwarded})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(provider.NamespaceEnvVar, "staging")
			t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct")
			t.Setenv("CLOUDFLARE_API_TOKEN", "tok")
			cloudflareZones(t)
			machine := &box{}
			written, err := routing.WriteRoutingTable(boxapi.RoutingTable{Grace: routing.DrainWindow})
			if err != nil {
				t.Fatal(err)
			}
			machine.routingDoc = string(written)
			p := over(machine)
			p.Recording(fake.NewKeyValues())

			err = read(t.Context(), p)

			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady || !strings.Contains(err.Error(), "ocel bootstrap") {
				t.Errorf("%s() on a box holding no origin id = %v, want it refused not ready, naming the bootstrap that mints one", name, err)
			}
			if machine.routingDoc != string(written) {
				t.Errorf("%s() rewrote the routing table to\n%s\nwant it untouched: reading what the proxy owns writes nothing on the box", name, machine.routingDoc)
			}
		})
	}
}
