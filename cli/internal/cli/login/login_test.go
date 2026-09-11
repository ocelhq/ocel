package login

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/console/credentials"
)

func TestRun(t *testing.T) {
	unreachable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(unreachable.Close)

	loggedInAt := func(apiURL string) cmddeps.Deps {
		deps := clitest.NewDeps()
		deps.LoadCredentials = func() (credentials.Credentials, error) {
			return credentials.Credentials{AccessToken: "tok", APIURL: apiURL, Email: "ada@example.com"}, nil
		}
		return deps
	}

	t.Run("a login to the console in effect is kept", func(t *testing.T) {
		t.Setenv(console.URLEnvVar, unreachable.URL)

		var out bytes.Buffer
		if err := run(context.Background(), loggedInAt(unreachable.URL+"/"), false, strings.NewReader(""), &out); err != nil {
			t.Fatalf("run err = %v", err)
		}
		if !strings.Contains(out.String(), "Already logged in as ada@example.com") {
			t.Fatalf("out = %q, want it to keep the existing login", out.String())
		}
	})

	t.Run("a login to another console starts a new one at OCEL_CONSOLE_URL", func(t *testing.T) {
		t.Setenv(console.URLEnvVar, unreachable.URL)

		var out bytes.Buffer
		err := run(context.Background(), loggedInAt("https://elsewhere.example.com"), false, strings.NewReader(""), &out)
		if err == nil || !strings.Contains(err.Error(), unreachable.URL) {
			t.Fatalf("run err = %v, want a login attempted at %s", err, unreachable.URL)
		}
		if strings.Contains(out.String(), "Already logged in") {
			t.Fatalf("out = %q, want no claim of an existing login", out.String())
		}
	})
}
