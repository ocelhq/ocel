package googletest

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

const (
	QuotaProjectHeader = "X-Goog-User-Project"
	tokenPath          = "/token"
)

func ServeAsGoogle(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	serveAsGoogle(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"ya29.stub","token_type":"Bearer","expires_in":3600}`))
	}, handler)
}

func ServeAsGoogleRefusingTokens(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	serveAsGoogle(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
	}, handler)
}

func serveAsGoogle(t *testing.T, answerToken func(http.ResponseWriter), handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			answerToken(w)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	redirected := http.DefaultTransport.(*http.Transport).Clone()
	redirected.Proxy = nil
	redirected.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	redirected.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	original := http.DefaultTransport
	http.DefaultTransport = redirected
	t.Cleanup(func() { http.DefaultTransport = original })
}

func UseUserCredential(t *testing.T) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"type":             "authorized_user",
		"client_id":        "id.apps.googleusercontent.com",
		"client_secret":    "secret",
		"refresh_token":    "refresh",
		"quota_project_id": "elsewhere",
		"token_uri":        "https://oauth2.googleapis.com" + tokenPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "adc.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "")
}
