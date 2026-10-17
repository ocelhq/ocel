package ports

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"cloud.google.com/go/auth"

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

func isolated(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLOUDSDK_CONFIG", filepath.Join(home, "gcloud"))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("GCE_METADATA_HOST", "")
	t.Setenv("GCE_METADATA_IP", "")
	return home
}

func claimsOf(t *testing.T, assertion string) map[string]any {
	t.Helper()
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion %q is no JWT", assertion)
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode the assertion: %v", err)
	}
	claims := map[string]any{}
	if err := json.Unmarshal(body, &claims); err != nil {
		t.Fatalf("decode the assertion claims: %v", err)
	}
	return claims
}

func serviceAccountKey(t *testing.T, dir, tokenURI string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: must(x509.MarshalPKCS8PrivateKey(key))})
	file, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "acme-prod",
		"private_key_id": "k1",
		"private_key":    string(encoded),
		"client_email":   "deployer@acme-prod.iam.gserviceaccount.com",
		"client_id":      "1",
		"token_uri":      tokenURI,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "key.json")
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func idToken(claims map[string]any) string {
	encode := func(part any) string { return base64.RawURLEncoding.EncodeToString(must(json.Marshal(part))) }
	return encode(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." + encode(claims) + ".c2lnbmVk"
}

func TestAServiceAccountKeyIssuesATokenForTheIdentityInfisicalNames(t *testing.T) {
	home := isolated(t)
	minted := idToken(map[string]any{"aud": "identity-1234", "email": "deployer@acme-prod.iam.gserviceaccount.com", "exp": time.Now().Add(time.Hour).Unix()})
	var mu sync.Mutex
	var audience string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse the token request: %v", err)
		}
		mu.Lock()
		audience, _ = claimsOf(t, r.PostForm.Get("assertion"))["target_audience"].(string)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": minted})
	}))
	defer server.Close()
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", serviceAccountKey(t, home, server.URL))

	proof, err := ProveIdentity(context.Background(), "identity-1234")
	if err != nil {
		t.Fatalf("ProveIdentity() = %v", err)
	}
	if proof.IDToken != minted || proof.SignedRequest != nil {
		t.Errorf("ProveIdentity() = %+v, want the id_token Google answered and nothing else", proof)
	}
	mu.Lock()
	defer mu.Unlock()
	if audience != "identity-1234" {
		t.Errorf("asked Google for an audience of %q, want the identity id Infisical checks the token's aud against", audience)
	}
}

func TestAThrottledOrFailingTokenEndpointIsAskedAgainAndARefusalIsNot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		answers  []int
		want     int
		refusing bool
	}{
		{"throttled then down then issued", []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusOK}, 3, false},
		{"refused", []int{http.StatusBadRequest, http.StatusOK}, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := isolated(t)
			minted := idToken(map[string]any{"aud": "identity-1234", "exp": time.Now().Add(time.Hour).Unix()})
			var mu sync.Mutex
			asked := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				answer := tc.answers[min(asked, len(tc.answers)-1)]
				asked++
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if answer != http.StatusOK {
					w.WriteHeader(answer)
					_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"id_token": minted})
			}))
			defer server.Close()
			t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", serviceAccountKey(t, home, server.URL))

			proof, err := ProveIdentity(context.Background(), "identity-1234")
			if tc.refusing && err == nil {
				t.Errorf("ProveIdentity() = %+v, want the refusal Google answered", proof)
			}
			if !tc.refusing && (err != nil || proof.IDToken != minted) {
				t.Errorf("ProveIdentity() = %+v, %v, want the token Google issued once it recovered", proof, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if asked != tc.want {
				t.Errorf("Google was asked %d times, want %d", asked, tc.want)
			}
		})
	}
}

type tokenFunc func(context.Context) (*auth.Token, error)

func (f tokenFunc) Token(ctx context.Context) (*auth.Token, error) { return f(ctx) }

func timesAsked(failure error) int {
	calls := 0
	credentials := auth.NewCredentials(&auth.CredentialsOptions{
		TokenProvider: tokenFunc(func(context.Context) (*auth.Token, error) { calls++; return nil, failure }),
	})
	_, _ = issued(context.Background(), credentials)
	return calls
}

func TestATokenFailureThatWillFailAgainIsAskedOnce(t *testing.T) {
	for name, failure := range map[string]error{
		"untrusted certificate": &url.Error{Op: "Post", URL: "https://oauth2.googleapis.com/token", Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}},
		"no such host":          &url.Error{Op: "Post", URL: "https://oauth2.googleapis.com/token", Err: &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "oauth2.example", IsNotFound: true}}},
		"connection refused":    &url.Error{Op: "Post", URL: "https://oauth2.googleapis.com/token", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}},
		"refused grant":         &auth.Error{Response: &http.Response{StatusCode: http.StatusBadRequest}},
	} {
		t.Run(name, func(t *testing.T) {
			if calls := timesAsked(failure); calls != 1 {
				t.Errorf("%s was asked %d times, want 1", name, calls)
			}
		})
	}
}

func TestATokenFailureThatMayPassIsAskedAgain(t *testing.T) {
	for name, failure := range map[string]error{
		"timed out":                 &url.Error{Op: "Post", URL: "https://oauth2.googleapis.com/token", Err: &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}},
		"connection reset":          &url.Error{Op: "Post", URL: "https://oauth2.googleapis.com/token", Err: &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}},
		"token endpoint down":       &auth.Error{Response: &http.Response{StatusCode: http.StatusServiceUnavailable}},
		"token endpoint throttling": &auth.Error{Response: &http.Response{StatusCode: http.StatusTooManyRequests}},
	} {
		t.Run(name, func(t *testing.T) {
			if calls := timesAsked(failure); calls != googleretry.Attempts {
				t.Errorf("%s was asked %d times, want %d", name, calls, googleretry.Attempts)
			}
		})
	}
}

func TestTheMetadataServerIssuesAFullTokenForTheIdentityInfisicalNames(t *testing.T) {
	isolated(t)
	var mu sync.Mutex
	var asked, flavor string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/identity") {
			asked, flavor = r.URL.RawQuery, r.Header.Get("Metadata-Flavor")
		}
		w.Header().Set("Metadata-Flavor", "Google")
		_, _ = w.Write([]byte("minted-by-metadata"))
	}))
	defer server.Close()
	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(server.URL, "http://"))

	proof, err := ProveIdentity(context.Background(), "identity-1234")
	if err != nil {
		t.Fatalf("ProveIdentity() = %v", err)
	}
	if proof.IDToken != "minted-by-metadata" {
		t.Errorf("ProveIdentity() = %+v, want the token the metadata server answered", proof)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(asked, "audience=identity-1234") || !strings.Contains(asked, "format=full") {
		t.Errorf("asked the metadata server for %q, want audience=identity-1234 and format=full", asked)
	}
	if flavor != "Google" {
		t.Errorf("Metadata-Flavor = %q, want Google", flavor)
	}
}

func TestAUserLoginIsRefusedWithWhatToDeployAsInstead(t *testing.T) {
	home := isolated(t)
	path := filepath.Join(home, "user.json")
	if err := os.WriteFile(path, []byte(`{"type":"authorized_user","client_id":"c","client_secret":"s","refresh_token":"r"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)

	_, err := ProveIdentity(context.Background(), "identity-1234")
	if err == nil || !strings.Contains(err.Error(), "service account") || !strings.Contains(err.Error(), "--impersonate-service-account") {
		t.Fatalf("ProveIdentity() under a user login = %v, want a refusal naming a service account to deploy as", err)
	}
}

func TestARequestGoogleThrottlesOrFailsIsSentAgainWithItsBodyAndARefusalIsNot(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []int
		want    int
		status  int
	}{
		{"throttled then down then answered", []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusOK}, 3, http.StatusOK},
		{"always down", []int{http.StatusServiceUnavailable}, 4, http.StatusServiceUnavailable},
		{"refused", []int{http.StatusForbidden, http.StatusOK}, 1, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var bodies []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				answer := tc.answers[min(len(bodies), len(tc.answers)-1)]
				bodies = append(bodies, string(body))
				mu.Unlock()
				w.WriteHeader(answer)
			}))
			defer server.Close()

			client := &http.Client{Transport: retriedTransport{base: http.DefaultTransport}}
			resp, err := client.Post(server.URL, "application/json", strings.NewReader(`{"audience":"identity-1234"}`))
			if err != nil {
				t.Fatalf("Post() = %v", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Errorf("answered %d, want %d", resp.StatusCode, tc.status)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) != tc.want {
				t.Errorf("Google was asked %d times, want %d", len(bodies), tc.want)
			}
			for _, body := range bodies {
				if body != `{"audience":"identity-1234"}` {
					t.Errorf("a request was sent again with body %q, want the one first sent", body)
				}
			}
		})
	}
}
