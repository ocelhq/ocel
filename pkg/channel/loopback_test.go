package channel

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestASessionTokenIsThirtyTwoRandomBytes(t *testing.T) {
	t.Parallel()

	first := NewSessionToken()
	raw, err := base64.RawURLEncoding.DecodeString(first)
	if err != nil {
		t.Fatalf("decode %q: %v", first, err)
	}
	if len(raw) != 32 {
		t.Errorf("token decoded to %d bytes, want 32", len(raw))
	}

	if second := NewSessionToken(); second == first {
		t.Error("two session tokens are the same")
	}
}

func guarded(t *testing.T, token string) *httptest.Server {
	t.Helper()

	var address string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		LoopbackGuard(address, token, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		})).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	address = srv.Listener.Addr().String()
	return srv
}

func TestALoopbackRequestCarryingTheTokenReachesTheHandler(t *testing.T) {
	t.Parallel()

	srv := guarded(t, "letmein")
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", FormatAuthHeader("letmein"))

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusTeapot)
	}
}

func TestALoopbackRequestWithoutAValidTokenIsRefused(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		header map[string]string
	}{
		{name: "carrying no token at all", header: nil},
		{name: "carrying the wrong token", header: map[string]string{"Authorization": FormatAuthHeader("guessed")}},
		{name: "carrying an unparseable authorization header", header: map[string]string{"Authorization": "letmein"}},
		{name: "coming from a foreign origin", header: map[string]string{
			"Authorization": FormatAuthHeader("letmein"),
			"Origin":        "http://evil.example",
		}},
		{name: "naming a host other than the listener", header: map[string]string{
			"Authorization": FormatAuthHeader("letmein"),
			"Host":          "rebound.example",
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := guarded(t, "letmein")
			req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			for name, value := range tc.header {
				if name == "Host" {
					req.Host = value
					continue
				}
				req.Header.Set(name, value)
			}

			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
			}
		})
	}
}
