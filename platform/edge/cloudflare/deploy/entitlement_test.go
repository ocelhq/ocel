package cloudflare

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func entitlementProvider(t *testing.T, subscriptions string, status int) *provider {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/subscriptions") {
			if status != http.StatusOK {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":9109,"message":"Unauthorized"}],"result":null}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":` + subscriptions + `}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"acct-1","name":"Acme"}}`))
	}))
	t.Cleanup(srv.Close)

	return &provider{client: cf.NewClient(
		option.WithBaseURL(srv.URL+"/"),
		option.WithAPIToken("test"),
	)}
}

func TestCodeEntitlementReportsWhatThePlanEntitles(t *testing.T) {
	for _, tc := range []struct {
		name          string
		subscriptions string
		status        int
		want          edge.Entitlement
		wantPlan      string
	}{
		{
			name:          "a Workers Paid subscription runs code at the edge",
			subscriptions: `[{"id":"sub-1","rate_plan":{"id":"workers_paid","public_name":"Workers Paid"}}]`,
			status:        http.StatusOK,
			want:          edge.EntitlementGranted,
			wantPlan:      "Workers Paid",
		},
		{
			name:          "an enterprise contract runs code at the edge",
			subscriptions: `[{"id":"sub-1","rate_plan":{"id":"enterprise","public_name":"Enterprise","is_contract":true}}]`,
			status:        http.StatusOK,
			want:          edge.EntitlementGranted,
		},
		{
			name:          "no Workers subscription withholds it",
			subscriptions: `[{"id":"sub-1","rate_plan":{"id":"pro","public_name":"Pro"}}]`,
			status:        http.StatusOK,
			want:          edge.EntitlementWithheld,
			wantPlan:      "Workers Free",
		},
		{
			name:          "an account with no subscriptions at all withholds it",
			subscriptions: `[]`,
			status:        http.StatusOK,
			want:          edge.EntitlementWithheld,
		},
		{
			name:   "a token that cannot read billing leaves it unknown",
			status: http.StatusForbidden,
			want:   edge.EntitlementUnknown,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envAccountID, "acct-1")
			t.Setenv(envAPIToken, "tok")

			p := entitlementProvider(t, tc.subscriptions, tc.status)
			got, err := p.CodeEntitlement(t.Context())
			if err != nil {
				t.Fatalf("CodeEntitlement: %v", err)
			}
			if got.Granted != tc.want {
				t.Errorf("Granted = %q, want %q", got.Granted, tc.want)
			}
			if tc.wantPlan != "" && got.Plan != tc.wantPlan {
				t.Errorf("Plan = %q, want %q", got.Plan, tc.wantPlan)
			}
		})
	}
}
