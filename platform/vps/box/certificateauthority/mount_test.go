package certificateauthority

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"
	endpointv1 "github.com/ocelhq/ocel/pkg/proto/endpoint/v1"
	"github.com/ocelhq/ocel/pkg/proto/endpoint/v1/endpointv1connect"
	"github.com/ocelhq/ocel/platform/vps/box/usercertificate"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

type issuer struct {
	url string
	key jwk.Key
}

func newIssuer(t *testing.T) *issuer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := jwk.Import(private)
	if err != nil {
		t.Fatal(err)
	}
	if err := key.Set(jwk.KeyIDKey, "key-1"); err != nil {
		t.Fatal(err)
	}
	if err := key.Set(jwk.AlgorithmKey, jwa.EdDSA()); err != nil {
		t.Fatal(err)
	}
	public, err := key.PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	set := jwk.NewSet()
	if err := set.AddKey(public); err != nil {
		t.Fatal(err)
	}
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(served.Close)
	return &issuer{url: served.URL, key: key}
}

func (i *issuer) credential(t *testing.T, target string, tier environment.Tier, expires time.Time) string {
	t.Helper()
	token, err := jwt.NewBuilder().
		Issuer(i.url).
		Audience([]string{"ocel-target:target-1"}).
		Subject("org:org-1:target:"+target+":tier:"+string(tier)+":project:project-1").
		Expiration(expires).
		IssuedAt(expires.Add(-10*time.Minute)).
		JwtID("4f1c2a3b-5d6e-4f70-8a9b-0c1d2e3f4a5b").
		Claim("org", "org-1").
		Claim("target", target).
		Claim("tier", string(tier)).
		Claim("project", "project-1").
		Claim("purpose", "deploy").
		Build()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jwt.Sign(token, jwt.WithKey(jwa.EdDSA(), i.key))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

type bearing struct {
	token string
	next  http.RoundTripper
}

func (b bearing) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(request)
}

func mounted(t *testing.T, trusted *issuer) *httptest.Server {
	t.Helper()
	authority, _ := signingAuthority(t, environment.TierProduction)
	authority.now = time.Now
	key := authorityKey(t)
	authority.sign = func(_ context.Context, request boxapi.CertificateRequest) (string, error) {
		return usercertificate.Sign(key, "ocel-deploy", request, time.Now())
	}
	handler, err := endpoint.New(endpoint.Spec{
		Config: endpoint.Config{
			Console:        trusted.url,
			EndpointID:     "endpoint-1",
			OrganizationID: "org-1",
			Credentials:    &endpoint.CredentialTrust{Issuer: trusted.url, Audience: "ocel-target:target-1", TargetID: "target-1"},
		},
		Mounts: []endpoint.Mount{authority.Mount()},
	})
	if err != nil {
		t.Fatal(err)
	}
	served := httptest.NewServer(handler)
	t.Cleanup(served.Close)
	return served
}

func requested(t *testing.T, served *httptest.Server, bearer string) error {
	t.Helper()
	line, _ := requestKey(t)
	client := endpointv1connect.NewSshCertificateServiceClient(&http.Client{Transport: bearing{token: bearer, next: served.Client().Transport}}, served.URL)
	_, err := client.IssueUserCertificate(context.Background(), &endpointv1.IssueUserCertificateRequest{PublicKey: line})
	return err
}

func TestTheMountedAuthoritySignsForTheConsolesCredentialForThisTarget(t *testing.T) {
	console := newIssuer(t)
	if err := requested(t, mounted(t, console), console.credential(t, "target-1", environment.TierProduction, time.Now().Add(10*time.Minute))); err != nil {
		t.Fatal(err)
	}
}

func TestTheMountedAuthorityRefusesACredentialItCannotTrust(t *testing.T) {
	console := newIssuer(t)
	foreign := newIssuer(t)
	for name, bearer := range map[string]string{
		"another target":   console.credential(t, "target-2", environment.TierProduction, time.Now().Add(10*time.Minute)),
		"an expired one":   console.credential(t, "target-1", environment.TierProduction, time.Now().Add(-time.Hour)),
		"a foreign issuer": foreign.credential(t, "target-1", environment.TierProduction, time.Now().Add(10*time.Minute)),
		"another tier":     console.credential(t, "target-1", environment.TierPreview, time.Now().Add(10*time.Minute)),
	} {
		t.Run(name, func(t *testing.T) {
			code := connect.CodeOf(requested(t, mounted(t, console), bearer))
			if code != connect.CodeUnauthenticated && code != connect.CodePermissionDenied {
				t.Fatalf("code = %v, want the call refused", code)
			}
		})
	}
}
