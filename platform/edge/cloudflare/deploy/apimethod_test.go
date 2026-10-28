package cloudflare

import (
	"net/http"
	"net/http/httptest"
	"testing"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/accounts"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"go.opentelemetry.io/otel/attribute"

	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

func TestACloudflareAPICallIsNamedForTheResourceItActsOnAndItsMethod(t *testing.T) {
	for _, call := range []struct {
		method, url, want string
	}{
		{http.MethodPut, "https://api.cloudflare.com/client/v4/accounts/023e105f4ecef8ad9ca31a8372d0c353/workers/scripts/shop-web", "accounts.workers.scripts.put"},
		{http.MethodPut, "https://api.cloudflare.com/client/v4/accounts/023e105f4ecef8ad9ca31a8372d0c353/workers/scripts/shop-web/secrets", "accounts.workers.scripts.secrets.put"},
		{http.MethodGet, "https://api.cloudflare.com/client/v4/accounts/023e105f4ecef8ad9ca31a8372d0c353/workers/scripts/shop-web/content/v2", "accounts.workers.scripts.content.get"},
		{http.MethodDelete, "https://api.cloudflare.com/client/v4/zones/023e105f4ecef8ad9ca31a8372d0c353/dns_records/372e67954025e0ba6aaa6d586b9e0b59", "zones.dns_records.delete"},
		{http.MethodPost, "https://api.cloudflare.com/client/v4/zones/023e105f4ecef8ad9ca31a8372d0c353/ssl/certificate_packs/order", "zones.ssl.certificate_packs.order.post"},
		{http.MethodGet, "https://api.cloudflare.com/client/v4/zones/023e105f4ecef8ad9ca31a8372d0c353/origin_tls_client_auth/hostnames/shop.app.com", "zones.origin_tls_client_auth.hostnames.get"},
		{http.MethodGet, "https://api.cloudflare.com/client/v4/accounts/023e105f4ecef8ad9ca31a8372d0c353/cfd_tunnel/f70ff985-a4ef-4643-bbbc-4a0ed4fc8415/token", "accounts.cfd_tunnel.token.get"},
		{http.MethodDelete, "https://api.cloudflare.com/client/v4/certificates/328578533902268680212849205732770752308931942346", "certificates.delete"},
		{http.MethodGet, "https://api.cloudflare.com/client/v4/zones?name=app.com", "zones.get"},
	} {
		t.Run(call.method+" "+call.url, func(t *testing.T) {
			req := httptest.NewRequest(call.method, call.url, nil)

			name, attrs, ok := nameAPIMethod(req)

			if !ok || name != "cloudflare/"+call.want {
				t.Errorf("nameAPIMethod() = %q, %v, want %q", name, ok, "cloudflare/"+call.want)
			}
			if !containsAttribute(attrs, "rpc.method", call.want) || !containsAttribute(attrs, "rpc.service", "cloudflare") {
				t.Errorf("nameAPIMethod() attributes = %v, want the service and method", attrs)
			}
		})
	}
}

func TestAPathWithNoResourceIsLeftToTheTransportsDefaultName(t *testing.T) {
	if name, _, ok := nameAPIMethod(httptest.NewRequest(http.MethodGet, "https://api.cloudflare.com/client/v4/", nil)); ok {
		t.Errorf("nameAPIMethod(the API root) = %q, want no name", name)
	}
}

func TestTheAPIClientNamesEachCallsSpanForItsAPIMethod(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(accountBody))
	}))
	t.Cleanup(server.Close)
	client := newAPIClient(option.WithBaseURL(server.URL+"/"), option.WithAPIToken("test"))
	ctx, spans := tracingtest.Record(t)

	if _, err := client.Accounts.Get(ctx, accounts.AccountGetParams{AccountID: cf.F("023e105f4ecef8ad9ca31a8372d0c353")}); err != nil {
		t.Fatal(err)
	}

	if span := spans.Only(t); span.Name() != "cloudflare/accounts.get" {
		t.Errorf("reading the account made span %q, want cloudflare/accounts.get", span.Name())
	}
}

func containsAttribute(attrs []attribute.KeyValue, key, value string) bool {
	for _, keyValue := range attrs {
		if string(keyValue.Key) == key && keyValue.Value.AsString() == value {
			return true
		}
	}
	return false
}
