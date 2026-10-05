package login

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/console"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
)

func TestLogoutSignsOutAtTheStoredConsoleAndDeletesTheStoredCredentials(t *testing.T) {
	t.Setenv(console.URLEnvVar, "")

	var signedOut string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signedOut = r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	dependencies := newTestDependencies()
	dependencies.LoadCredentials = func() (console.Credentials, error) {
		return console.Credentials{AccessToken: "tok", APIURL: srv.URL + "/"}, nil
	}
	deleted := false
	dependencies.DeleteCredentials = func() error {
		deleted = true
		return nil
	}

	if err := runLogout(context.Background(), dependencies, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runLogout err = %v", err)
	}
	if signedOut != "POST /api/auth/sign-out Bearer tok" {
		t.Errorf("sign-out request = %q, want POST /api/auth/sign-out with the stored token", signedOut)
	}
	if !deleted {
		t.Error("the stored credentials were not deleted")
	}
}

func TestLogoutAsJSONSaysWhetherTheSessionWasRevokedAtTheConsole(t *testing.T) {
	for name, status := range map[string]int{"revoked": http.StatusOK, "refused": http.StatusInternalServerError} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(console.URLEnvVar, "")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
			t.Cleanup(srv.Close)
			dependencies := newTestDependencies()
			dependencies.Presentation = clitest.ResolveJSONPresentation
			dependencies.LoadCredentials = func() (console.Credentials, error) {
				return console.Credentials{AccessToken: "tok", APIURL: srv.URL + "/"}, nil
			}
			dependencies.DeleteCredentials = func() error { return nil }

			var stdout, stderr bytes.Buffer
			if err := runLogout(context.Background(), dependencies, &stdout, &stderr); err != nil {
				t.Fatalf("runLogout err = %v", err)
			}

			var got resultv1.LogoutResult
			clitest.DecodeResultInto(t, stdout.String(), &got)
			if !got.GetLoggedOut() || got.GetSessionRevoked() != (status == http.StatusOK) {
				t.Errorf("logout result = %v, want credentials cleared and the session revoked only when the console accepted it", &got)
			}
		})
	}
}

func TestLogoutAsJSONWhenNotLoggedInSaysNothingWasLoggedOut(t *testing.T) {
	dependencies := newTestDependencies()
	dependencies.Presentation = clitest.ResolveJSONPresentation
	dependencies.LoadCredentials = func() (console.Credentials, error) { return console.Credentials{}, console.ErrNotLoggedIn }

	var stdout bytes.Buffer
	if err := runLogout(context.Background(), dependencies, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("runLogout err = %v", err)
	}

	var got resultv1.LogoutResult
	clitest.DecodeResultInto(t, stdout.String(), &got)
	if got.GetLoggedOut() || got.GetSessionRevoked() {
		t.Errorf("logout result = %v, want nothing logged out", &got)
	}
}
