package login

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/console"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
)

func TestLoginKeepsTheConsoleInEffectAndStartsANewOneElsewhere(t *testing.T) {
	unreachable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(unreachable.Close)

	loggedInAt := func(apiURL string) Dependencies {
		dependencies := newTestDependencies()
		dependencies.LoadCredentials = func() (console.Credentials, error) {
			return console.Credentials{AccessToken: "tok", APIURL: apiURL, Email: "ada@example.com"}, nil
		}
		return dependencies
	}

	t.Run("a login to the console in effect is kept", func(t *testing.T) {
		t.Setenv(console.URLEnvVar, unreachable.URL)

		var out bytes.Buffer
		if err := run(context.Background(), loggedInAt(unreachable.URL+"/"), false, strings.NewReader(""), &out, &bytes.Buffer{}); err != nil {
			t.Fatalf("run err = %v", err)
		}
		if !strings.Contains(out.String(), "Already logged in as ada@example.com") {
			t.Fatalf("out = %q, want it to keep the existing login", out.String())
		}
	})

	t.Run("a login to another console starts a new one at OCEL_CONSOLE_URL", func(t *testing.T) {
		t.Setenv(console.URLEnvVar, unreachable.URL)

		var out bytes.Buffer
		err := run(context.Background(), loggedInAt("https://elsewhere.example.com"), false, strings.NewReader(""), &out, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), unreachable.URL) {
			t.Fatalf("run err = %v, want a login attempted at %s", err, unreachable.URL)
		}
		if strings.Contains(out.String(), "Already logged in") {
			t.Fatalf("out = %q, want no claim of an existing login", out.String())
		}
	})
}

func TestLoginAsJSONPrintsTheAccountAndTheConsoleAndNeverTheToken(t *testing.T) {
	consoleServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/device/code":
			_, _ = w.Write([]byte(`{"device_code":"dev","user_code":"ABCD1234","verification_uri":"https://console.example.com/device","verification_uri_complete":"https://console.example.com/device?code=ABCD1234","expires_in":600,"interval":1}`))
		case "/api/auth/device/token":
			_, _ = w.Write([]byte(`{"access_token":"secret-token","token_type":"Bearer","expires_in":3600}`))
		case "/api/auth/get-session":
			_, _ = w.Write([]byte(`{"session":{},"user":{"email":"ada@example.com","name":"Ada"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(consoleServer.Close)
	t.Setenv(console.URLEnvVar, consoleServer.URL)

	dependencies := newTestDependencies()
	dependencies.Presentation = clitest.ResolveJSONPresentation
	dependencies.LoadCredentials = func() (console.Credentials, error) { return console.Credentials{}, console.ErrNotLoggedIn }
	dependencies.SaveCredentials = func(console.Credentials) (console.CredentialStore, error) { return console.FileStore, nil }

	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), dependencies, false, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	var got resultv1.LoginResult
	clitest.DecodeResultInto(t, stdout.String(), &got)
	if got.GetEmail() != "ada@example.com" || got.GetConsoleUrl() != consoleServer.URL {
		t.Errorf("login result = %v, want ada@example.com at %s", &got, consoleServer.URL)
	}
	if strings.Contains(stdout.String(), "secret-token") || strings.Contains(stderr.String(), "secret-token") {
		t.Errorf("stdout = %q, stderr = %q, want no token", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "ABCD-1234") || !strings.Contains(stderr.String(), "https://console.example.com/device?code=ABCD1234") {
		t.Errorf("stderr = %q, want the code to confirm and where to confirm it", stderr.String())
	}
}

func TestLoginAsJSONOfAnExistingLoginPrintsItsAccount(t *testing.T) {
	t.Setenv(console.URLEnvVar, "https://console.example.com")
	dependencies := newTestDependencies()
	dependencies.Presentation = clitest.ResolveJSONPresentation
	dependencies.LoadCredentials = func() (console.Credentials, error) {
		return console.Credentials{AccessToken: "secret-token", APIURL: "https://console.example.com/", Email: "ada@example.com"}, nil
	}

	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), dependencies, false, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run err = %v", err)
	}

	var got resultv1.LoginResult
	clitest.DecodeResultInto(t, stdout.String(), &got)
	if got.GetEmail() != "ada@example.com" || got.GetConsoleUrl() != "https://console.example.com" {
		t.Errorf("login result = %v, want the stored account and console", &got)
	}
	if strings.Contains(stdout.String(), "secret-token") {
		t.Errorf("stdout = %q, want no token", stdout.String())
	}
}
