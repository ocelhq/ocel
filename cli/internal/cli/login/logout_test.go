package login

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/console"
)

func TestLogoutSignsOutAtTheStoredConsoleAndDeletesTheStoredCredentials(t *testing.T) {
	t.Setenv(console.URLEnvVar, "")

	var signedOut string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signedOut = r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	deps := clitest.NewDeps()
	deps.LoadCredentials = func() (console.Credentials, error) {
		return console.Credentials{AccessToken: "tok", APIURL: srv.URL + "/"}, nil
	}
	deleted := false
	deps.DeleteCredentials = func() error {
		deleted = true
		return nil
	}

	if err := runLogout(context.Background(), deps, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runLogout err = %v", err)
	}
	if signedOut != "POST /api/auth/sign-out Bearer tok" {
		t.Errorf("sign-out request = %q, want POST /api/auth/sign-out with the stored token", signedOut)
	}
	if !deleted {
		t.Error("the stored credentials were not deleted")
	}
}
