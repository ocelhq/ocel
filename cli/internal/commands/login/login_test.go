package login

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/console"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
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

type describedConsole struct {
	consolev1connect.UnimplementedConsoleServiceHandler
	cloud *consolev1.CloudOffer
}

func (d *describedConsole) Describe(context.Context, *consolev1.DescribeRequest) (*consolev1.DescribeResponse, error) {
	return &consolev1.DescribeResponse{ConsoleVersion: "1.0.0", Cloud: d.cloud}, nil
}

func serveDeviceLogin(t *testing.T, describe consolev1connect.ConsoleServiceHandler) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/device/code", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"device_code":"dev","user_code":"ABCD1234","verification_uri":"https://console.example.com/device","expires_in":600,"interval":1}`))
	})
	mux.HandleFunc("/api/auth/device/token", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"secret-token","token_type":"Bearer","expires_in":3600}`))
	})
	mux.HandleFunc("/api/auth/get-session", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"session":{},"user":{"email":"ada@example.com","name":"Ada"}}`))
	})
	if describe != nil {
		path, handler := consolev1connect.NewConsoleServiceHandler(describe)
		mux.Handle("/api/connect"+path, http.StripPrefix("/api/connect", handler))
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestLoginSavesWhetherTheConsoleOffersOcelCloudAndSaysSo(t *testing.T) {
	cases := []struct {
		name      string
		describe  consolev1connect.ConsoleServiceHandler
		wantCloud *console.CloudOffer
		wantLine  string
	}{
		{
			name:      "a console that offers Ocel Cloud",
			describe:  &describedConsole{cloud: &consolev1.CloudOffer{ApiUrl: "https://cloud.example.test", Audience: "ocel-cloud"}},
			wantCloud: &console.CloudOffer{APIURL: "https://cloud.example.test", Audience: "ocel-cloud"},
			wantLine:  "offers Ocel Cloud at https://cloud.example.test.",
		},
		{
			name:     "a console that offers none",
			describe: &describedConsole{},
			wantLine: "offers no Ocel Cloud.",
		},
		{
			name:     "a console that cannot describe itself",
			wantLine: "offers no Ocel Cloud.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := serveDeviceLogin(t, tc.describe)
			t.Setenv(console.URLEnvVar, url)

			var saved console.Credentials
			dependencies := newTestDependencies()
			dependencies.LoadCredentials = func() (console.Credentials, error) { return console.Credentials{}, console.ErrNotLoggedIn }
			dependencies.SaveCredentials = func(creds console.Credentials) (console.CredentialStore, error) {
				saved = creds
				return console.KeyringStore, nil
			}

			before := time.Now()
			var out bytes.Buffer
			if err := run(context.Background(), dependencies, false, strings.NewReader(""), &out, &bytes.Buffer{}); err != nil {
				t.Fatalf("run err = %v; out=%s", err, out.String())
			}

			if (saved.Cloud == nil) != (tc.wantCloud == nil) || saved.Cloud != nil && *saved.Cloud != *tc.wantCloud {
				t.Errorf("saved offer = %+v, want %+v", saved.Cloud, tc.wantCloud)
			}
			if saved.CloudFetchedAt.Before(before) {
				t.Errorf("saved fetched at %v, want the time of this login", saved.CloudFetchedAt)
			}
			if want := url + " " + tc.wantLine; !strings.Contains(out.String(), want) {
				t.Errorf("out = %q, want it to say %q", out.String(), want)
			}
		})
	}
}

func TestLoginOfAnExistingLoginSaysWhetherTheConsoleOffersOcelCloud(t *testing.T) {
	t.Setenv(console.URLEnvVar, "https://console.example.com")
	dependencies := newTestDependencies()
	dependencies.LoadCredentials = func() (console.Credentials, error) {
		return console.Credentials{
			AccessToken:    "tok",
			APIURL:         "https://console.example.com",
			Email:          "ada@example.com",
			Store:          console.KeyringStore,
			Cloud:          &console.CloudOffer{APIURL: "https://cloud.example.test", Audience: "ocel-cloud"},
			CloudFetchedAt: time.Now(),
		}, nil
	}

	var out bytes.Buffer
	if err := run(context.Background(), dependencies, false, strings.NewReader(""), &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("run err = %v", err)
	}
	if want := "https://console.example.com offers Ocel Cloud at https://cloud.example.test."; !strings.Contains(out.String(), want) {
		t.Errorf("out = %q, want it to say %q", out.String(), want)
	}
}
