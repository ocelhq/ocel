package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
)

type tunnelMock struct {
	held          []map[string]any
	created       []map[string]any
	takenByRace   bool
	configured    map[string]any
	tokenReads    []string
	cleaned       []string
	deleted       []string
	connectedOnce bool
	nameQueries   []string
}

func (m *cfMock) serveTunnels(mux *http.ServeMux) {
	answer := func(w http.ResponseWriter, status int, result any, errs ...map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if errs == nil {
			errs = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": status/100 == 2, "errors": errs, "messages": []any{}, "result": result,
			"result_info": map[string]any{"page": 1, "per_page": 100, "count": 1, "total_count": 1},
		})
	}
	named := func(name string) []map[string]any {
		matched := []map[string]any{}
		for _, tunnel := range m.tunnels.held {
			if tunnel["name"] == name {
				matched = append(matched, tunnel)
			}
		}
		return matched
	}
	mux.HandleFunc("GET /accounts/acct/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		m.tunnels.nameQueries = append(m.tunnels.nameQueries, r.URL.Query().Get("name")+" deleted="+r.URL.Query().Get("is_deleted"))
		if page := r.URL.Query().Get("page"); page != "" && page != "1" {
			answer(w, http.StatusOK, []any{})
			return
		}
		answer(w, http.StatusOK, named(r.URL.Query().Get("name")))
	})
	mux.HandleFunc("POST /accounts/acct/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if m.tunnels.takenByRace {
			m.tunnels.takenByRace = false
			m.tunnels.held = append(m.tunnels.held, map[string]any{"id": "0f1e2d3c-raced", "name": body["name"]})
		}
		if name, _ := body["name"].(string); len(named(name)) > 0 {
			answer(w, http.StatusBadRequest, nil, map[string]any{"code": 1013, "message": "You already have a tunnel with this name."})
			return
		}
		m.tunnels.created = append(m.tunnels.created, body)
		created := map[string]any{"id": fmt.Sprintf("5a6b7c8d-%d", len(m.tunnels.created)), "name": body["name"]}
		m.tunnels.held = append(m.tunnels.held, created)
		answer(w, http.StatusOK, created)
	})
	mux.HandleFunc("PUT /accounts/acct/cfd_tunnel/{id}/configurations", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if m.tunnels.configured == nil {
			m.tunnels.configured = map[string]any{}
		}
		m.tunnels.configured[r.PathValue("id")] = body["config"]
		answer(w, http.StatusOK, map[string]any{"tunnel_id": r.PathValue("id")})
	})
	mux.HandleFunc("GET /accounts/acct/cfd_tunnel/{id}/token", func(w http.ResponseWriter, r *http.Request) {
		m.tunnels.tokenReads = append(m.tunnels.tokenReads, r.PathValue("id"))
		answer(w, http.StatusOK, "token-for-"+r.PathValue("id"))
	})
	mux.HandleFunc("DELETE /accounts/acct/cfd_tunnel/{id}/connections", func(w http.ResponseWriter, r *http.Request) {
		m.tunnels.cleaned = append(m.tunnels.cleaned, r.PathValue("id"))
		m.tunnels.connectedOnce = false
		answer(w, http.StatusOK, map[string]any{})
	})
	mux.HandleFunc("DELETE /accounts/acct/cfd_tunnel/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if m.tunnels.connectedOnce {
			answer(w, http.StatusBadRequest, nil, map[string]any{"code": 1022, "message": "Cannot delete tunnel because it has active connections."})
			return
		}
		held := slices.IndexFunc(m.tunnels.held, func(tunnel map[string]any) bool { return tunnel["id"] == id })
		if held < 0 {
			answer(w, http.StatusNotFound, nil, map[string]any{"code": 1003, "message": "Tunnel not found"})
			return
		}
		m.tunnels.held = slices.Delete(m.tunnels.held, held, held+1)
		m.tunnels.deleted = append(m.tunnels.deleted, id)
		answer(w, http.StatusOK, map[string]any{"id": id})
	})
}

func tunnelHooks(t *testing.T, m *cfMock) *edge.TunnelHooks {
	t.Helper()
	tunnels := m.proxy(t).Hooks().Tunnels
	if tunnels == nil {
		t.Fatal("Hooks().Tunnels = nil, want the Cloudflare proxy to open tunnels")
	}
	return tunnels
}

func TestTheCloudflareProxyOpensARemotelyManagedTunnelUnderANameOnce(t *testing.T) {
	m := proxyZoneMock()
	tunnels := tunnelHooks(t, m)

	first, err := tunnels.Ensure(context.Background(), "ocel-box-203.0.113.9")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	again, err := tunnels.Ensure(context.Background(), "ocel-box-203.0.113.9")
	if err != nil {
		t.Fatalf("Ensure again: %v", err)
	}

	if len(m.tunnels.created) != 1 || m.tunnels.created[0]["config_src"] != "cloudflare" || m.tunnels.created[0]["name"] != "ocel-box-203.0.113.9" {
		t.Fatalf("created tunnels %v, want one named ocel-box-203.0.113.9 and configured from Cloudflare: its ingress is set through the API, never in a file on the box", m.tunnels.created)
	}
	want := edge.Tunnel{ID: "5a6b7c8d-1", Address: "5a6b7c8d-1.cfargotunnel.com"}
	if first != want || again != want {
		t.Errorf("Ensure = %+v then %+v, want %+v both times: a proxied record names the tunnel by its cfargotunnel.com address", first, again, want)
	}
	for _, query := range m.tunnels.nameQueries {
		if query != "ocel-box-203.0.113.9 deleted=false" {
			t.Errorf("the tunnels were listed as %q, want them read by name, leaving deleted ones out", query)
		}
	}
}

func TestTheCloudflareProxyTakesTheTunnelAnotherRunOpenedUnderTheSameName(t *testing.T) {
	m := proxyZoneMock()
	m.tunnels.takenByRace = true
	tunnels := tunnelHooks(t, m)

	opened, err := tunnels.Ensure(context.Background(), "ocel-box-203.0.113.9")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if opened.ID != "0f1e2d3c-raced" || len(m.tunnels.held) != 1 {
		t.Errorf("Ensure = %+v with tunnels %v held, want the one the other run opened: one tunnel serves one box", opened, m.tunnels.held)
	}
}

func TestTheCloudflareProxySendsEveryHostnameThroughATunnelToOneService(t *testing.T) {
	m := proxyZoneMock()
	tunnels := tunnelHooks(t, m)

	if err := tunnels.Configure(context.Background(), "5a6b7c8d-1", "http://switchboard:8444"); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	encoded, _ := json.Marshal(m.tunnels.configured["5a6b7c8d-1"])
	if string(encoded) != `{"ingress":[{"service":"http://switchboard:8444"}]}` {
		t.Errorf("the tunnel was configured with %s, want one catch-all ingress rule to the service: the origin decides what each hostname is", encoded)
	}
}

func TestTheCloudflareProxyReadsTheTokenATunnelRunsWith(t *testing.T) {
	m := proxyZoneMock()
	tunnels := tunnelHooks(t, m)

	token, err := tunnels.ReadToken(context.Background(), "5a6b7c8d-1")
	if err != nil {
		t.Fatalf("ReadToken: %v", err)
	}
	if token != "token-for-5a6b7c8d-1" {
		t.Errorf("ReadToken = %q, want the token Cloudflare holds for the tunnel", token)
	}
}

func TestTheCloudflareProxyDeletesATunnelOnceItsConnectionsAreCleanedUp(t *testing.T) {
	m := proxyZoneMock()
	m.tunnels.held = []map[string]any{{"id": "5a6b7c8d-1", "name": "ocel-box-203.0.113.9"}}
	m.tunnels.connectedOnce = true
	tunnels := tunnelHooks(t, m)

	if err := tunnels.Delete(context.Background(), "5a6b7c8d-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := tunnels.Delete(context.Background(), "5a6b7c8d-1"); err != nil {
		t.Fatalf("Delete of a tunnel already gone: %v", err)
	}

	if !slices.Equal(m.tunnels.cleaned, []string{"5a6b7c8d-1", "5a6b7c8d-1"}) || !slices.Equal(m.tunnels.deleted, []string{"5a6b7c8d-1"}) {
		t.Errorf("cleaned %v and deleted %v, want the connections cleaned before each delete: Cloudflare refuses to delete a tunnel with a connection left", m.tunnels.cleaned, m.tunnels.deleted)
	}
}

func TestTheCloudflareProxyForwardsAHostnameToATunnelWithoutCheckingTheZonesSSLMode(t *testing.T) {
	m := proxyZoneMock()
	m.sslMode = "flexible"
	_, stack := reconciledProxy(t, m)

	err := stack.BindDomain(context.Background(), edge.DomainBinding{
		Hostname: "shop.app.com",
		Origin:   &edge.Origin{Address: "5a6b7c8d-1.cfargotunnel.com", Certified: true, Tunneled: true},
	})
	if err != nil {
		t.Fatalf("BindDomain through a tunnel on a zone in flexible mode: %v", err)
	}

	if len(m.createdRecords) != 1 || m.createdRecords[0]["type"] != "CNAME" || m.createdRecords[0]["content"] != "5a6b7c8d-1.cfargotunnel.com" || m.createdRecords[0]["proxied"] != true {
		t.Errorf("wrote records %v, want one proxied CNAME to the tunnel: the tunnel is the only way to the origin, so no SSL mode decides what Cloudflare checks there", m.createdRecords)
	}
}

func TestTheCloudflareProxyForwardsThePreviewWildcardToATunnelWithOneProxiedRecord(t *testing.T) {
	m := proxyZoneMock()
	m.sslMode = "flexible"
	m.certificatePacks = []map[string]any{activePack("app.com", "*.preview.app.com")}
	tunnel := &edge.Origin{Address: "5a6b7c8d-1.cfargotunnel.com", Certified: true, Tunneled: true}

	if _, err := m.proxy(t).ReconcilePreviewWildcard(context.Background(), edge.PreviewWildcardSpec{BaseDomain: "preview.app.com", Origin: tunnel}); err != nil {
		t.Fatalf("ReconcilePreviewWildcard through a tunnel: %v", err)
	}

	if len(m.createdRecords) != 1 || m.createdRecords[0]["name"] != "*.preview.app.com" || m.createdRecords[0]["type"] != "CNAME" || m.createdRecords[0]["content"] != tunnel.Address || m.createdRecords[0]["proxied"] != true {
		t.Errorf("wrote records %v, want one proxied wildcard CNAME to the tunnel", m.createdRecords)
	}
}

func TestTheCloudflareProxysTokenNeedsTunnelWriteOnTheAccountOnlyForATunnel(t *testing.T) {
	doc, err := NewProxy("ocel").Hooks().DescribeCredentialPermissions(edge.PurposeDeploy)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Document, "Account · Cloudflare Tunnel · Edit (only when the edge sets `tunnel`)") {
		t.Errorf("the proxy's permissions are %q, want Cloudflare Tunnel edit on the account, marked as needed only for a tunnel: a tunnel is opened, configured and deleted there, and a token without `tunnel` never touches one", doc.Document)
	}
}
