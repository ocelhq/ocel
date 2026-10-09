package cloudflare

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestVerifyingCredentialsAsksCloudflareForTheAccountUnlessTheChecksAreSkipped(t *testing.T) {
	t.Setenv(envAccountID, "acct")
	t.Setenv(envAPIToken, "tok")

	m := &cfMock{zoneID: "zone1", zoneName: "app.com"}
	_, _ = m.provider(t).Hooks().VerifyCredentials(t.Context())
	if m.requests == 0 {
		t.Error("VerifyCredentials() sent Cloudflare nothing, want the account read to prove the token")
	}

	m = &cfMock{zoneID: "zone1", zoneName: "app.com"}
	p := m.provider(t)
	p.skipChecks = true
	identity, err := p.Hooks().VerifyCredentials(t.Context())
	if err != nil {
		t.Fatalf("VerifyCredentials() with the checks skipped error = %v", err)
	}
	if m.requests != 0 {
		t.Errorf("VerifyCredentials() with the checks skipped sent Cloudflare %d requests, want none", m.requests)
	}
	if identity.Account != "acct" {
		t.Errorf("VerifyCredentials() with the checks skipped answered account %q, want the one the environment names", identity.Account)
	}
}

func TestAnEdgeSkipsItsChecksOnlyWhenTheEnvironmentSaysSo(t *testing.T) {
	t.Setenv(processenv.SkipChecksEnvVar, "1")
	if !newCloudflare("ocel", Options{}).skipChecks {
		t.Errorf("an edge opened with %s=1 runs its checks, want them skipped", processenv.SkipChecksEnvVar)
	}
	t.Setenv(processenv.SkipChecksEnvVar, "")
	if newCloudflare("ocel", Options{}).skipChecks {
		t.Errorf("an edge opened without %s skips its checks, want them run", processenv.SkipChecksEnvVar)
	}
}

func TestCredentialPermissionsListsWhatEachPurposeMints(t *testing.T) {
	for _, tc := range []struct {
		name    string
		purpose edge.CredentialPurpose
		want    []string
		gone    []string
	}{
		{
			name:    "bootstrap mints the R2 access token, so it edits API tokens",
			purpose: edge.PurposeBootstrap,
			want: []string{
				"User · API Tokens · Edit",
				"Account · Workers Scripts · Edit",
				"Zone · Workers Routes · Edit",
			},
		},
		{
			name:    "deploy mints nothing, so it never edits API tokens",
			purpose: edge.PurposeDeploy,
			want: []string{
				"Account · Workers Scripts · Edit",
				"Zone · Workers Routes · Edit",
			},
			gone: []string{"User · API Tokens · Edit"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := New("ocel", Options{}).Hooks().DescribeCredentialPermissions(tc.purpose)
			if err != nil {
				t.Fatalf("DescribeCredentialPermissions(%v) error = %v", tc.purpose, err)
			}
			if doc.Heading != "Cloudflare API token" {
				t.Errorf("DescribeCredentialPermissions(%v) heading = %q, want the Cloudflare token named", tc.purpose, doc.Heading)
			}
			for _, want := range tc.want {
				if !strings.Contains(doc.Document, want) {
					t.Errorf("DescribeCredentialPermissions(%v) = %q, want it to include %q", tc.purpose, doc.Document, want)
				}
			}
			for _, gone := range tc.gone {
				if strings.Contains(doc.Document, gone) {
					t.Errorf("DescribeCredentialPermissions(%v) = %q, want %q left out", tc.purpose, doc.Document, gone)
				}
			}
		})
	}

	if _, err := New("ocel", Options{}).Hooks().DescribeCredentialPermissions("admin"); err == nil || !strings.Contains(err.Error(), `"admin"`) {
		t.Errorf("DescribeCredentialPermissions(admin) err = %v, want it to name the purpose it was asked for", err)
	}
}

func TestAnEdgeOpenedForAMutualTLSOriginsBootstrapTokenMayEditAccountCertificates(t *testing.T) {
	const permission = "Account · SSL and Certificates · Edit"
	mutual := newCloudflare("ocel", Options{})
	mutual.workerClientCertificate = true

	for _, tc := range []struct {
		name    string
		edge    edge.Edge
		purpose edge.CredentialPurpose
		want    bool
	}{
		{"mutual TLS bootstrap", mutual, edge.PurposeBootstrap, true},
		{"mutual TLS deploy", mutual, edge.PurposeDeploy, false},
		{"plain bootstrap", New("ocel", Options{}), edge.PurposeBootstrap, false},
		{"plain deploy", New("ocel", Options{}), edge.PurposeDeploy, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := tc.edge.Hooks().DescribeCredentialPermissions(tc.purpose)
			if err != nil {
				t.Fatalf("DescribeCredentialPermissions(%v) error = %v", tc.purpose, err)
			}
			if got := strings.Contains(doc.Document, permission); got != tc.want {
				t.Errorf("DescribeCredentialPermissions(%v) = %q, includes %q = %v, want %v", tc.purpose, doc.Document, permission, got, tc.want)
			}
		})
	}
}

func verifiedAgainst(t *testing.T, answers ...answer) error {
	t.Helper()
	t.Setenv(envAccountID, "acct-1")
	t.Setenv(envAPIToken, "test")
	client, _, _ := scriptedClient(t, answers...)
	_, err := (&cloudflare{namespace: "ocel", client: client}).verifyCredentials(context.Background())
	return err
}

func TestVerifyCredentialsReturnsTheCancellationOfACheckItWasStoppedFrom(t *testing.T) {
	t.Setenv(envAccountID, "acct-1")
	t.Setenv(envAPIToken, "test")
	client, _, _ := scriptedClient(t, answer{status: http.StatusOK, body: accountBody})
	for _, tc := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		want error
	}{
		{"cancelled", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, cancel
		}, context.Canceled},
		{"past its deadline", func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		}, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.ctx()
			defer cancel()
			_, err := (&cloudflare{namespace: "ocel", client: client}).verifyCredentials(ctx)
			var refused refusal.Refusal
			if !errors.Is(err, tc.want) || errors.As(err, &refused) {
				t.Errorf("verifyCredentials() error = %#v, want %v itself, not a refusal that says Cloudflare could not be reached", err, tc.want)
			}
			dns := &dnsRecords{zones: client.Zones, accountID: "acct-1"}
			err = dns.VerifyCredentials(ctx)
			if !errors.Is(err, tc.want) || errors.As(err, &refused) {
				t.Errorf("dnsRecords.VerifyCredentials() error = %#v, want %v itself", err, tc.want)
			}
		})
	}
}

func TestVerifyCredentialsSaysARateLimitedAccountIsRateLimitedNotThatItsTokenWasRejected(t *testing.T) {
	err := verifiedAgainst(t, answer{status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "600"}, body: rateLimitedBody})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Fatalf("verifyCredentials() error = %#v, want a busy refusal", err)
	}
	if strings.Contains(err.Error(), "rejected") {
		t.Errorf("verifyCredentials() error = %q, want no claim the token was rejected", err)
	}
	if !strings.Contains(err.Error(), "rate limiting") {
		t.Errorf("verifyCredentials() error = %q, want it to say the account is being rate limited", err)
	}
}

func TestVerifyCredentialsSaysATokenCloudflareRefusesWasRejected(t *testing.T) {
	err := verifiedAgainst(t, answer{status: http.StatusForbidden, body: `{"success":false,"errors":[{"code":9109,"message":"Unauthorized to access requested resource"}]}`})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("verifyCredentials() error = %#v, want a denied refusal saying the token was rejected", err)
	}
}

func TestVerifyCredentialsSaysCloudflareCouldNotBeReachedWhenItKeepsFailing(t *testing.T) {
	err := verifiedAgainst(t, answer{status: http.StatusServiceUnavailable, body: `{"success":false,"errors":[{"code":10000,"message":"unavailable"}]}`})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("verifyCredentials() error = %#v, want a not-ready refusal", err)
	}
	if strings.Contains(err.Error(), "rejected") {
		t.Errorf("verifyCredentials() error = %q, want no claim the token was rejected", err)
	}
}
