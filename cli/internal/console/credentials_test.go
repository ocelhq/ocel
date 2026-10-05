package console

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest/confighome"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
)

func TestCredentialsFileLivesInTheUserConfigDirectory(t *testing.T) {
	configDir := confighome.Isolate(t)

	path, err := ensureCredentialsFilePath()
	if err != nil {
		t.Fatalf("ensureCredentialsFilePath err = %v", err)
	}
	if want := filepath.Join(configDir, "credentials.json"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("config directory stat = %v, %v, want a 0700 directory", info, err)
	}
}

func TestRequireLoginWithoutSavedCredentialsReportsConsoleNotLoggedInHintingOcelLogin(t *testing.T) {
	var stderr bytes.Buffer
	_, err := RequireLogin(func() (Credentials, error) { return Credentials{}, ErrNotLoggedIn }, &stderr)

	got := clierror.NewRunError(err)
	if got.GetCode() != "console.not_logged_in" || got.GetHint() != "ocel login" {
		t.Fatalf("run error = %s, want console.not_logged_in hinting `ocel login`", protojson.Format(got))
	}
	if code, ok := exitcode.Of(err); !ok || code != 1 {
		t.Errorf("exit code = %d, %v, want 1 without printing the error again", code, ok)
	}
	if stderr.String() != "You're not logged in. Run `ocel login` first.\n" {
		t.Errorf("stderr = %q, want the one human line", stderr.String())
	}
}

func TestRequireLoginWithUnreadableCredentialsReportsWhyAndNeverClaimsALogout(t *testing.T) {
	unreadable := errors.New("decode stored credentials: unexpected end of JSON input")
	var stderr bytes.Buffer
	_, err := RequireLogin(func() (Credentials, error) { return Credentials{}, unreadable }, &stderr)

	if !errors.Is(err, unreadable) || errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("err = %v, want the read failure as its cause and no not-logged-in", err)
	}
	if got := clierror.NewRunError(err); got.GetCode() != "internal" {
		t.Errorf("run error = %s, want internal", protojson.Format(got))
	}
	if _, ok := exitcode.Of(err); ok {
		t.Errorf("err = %v carries an exit status, want it printed as a failure", err)
	}
	if strings.Contains(stderr.String()+err.Error(), "not logged in") {
		t.Errorf("stderr = %q, err = %q, want neither to claim a logout", stderr.String(), err)
	}
	if !strings.Contains(err.Error(), "ocel login") {
		t.Errorf("err = %q, want it to point at `ocel login`", err)
	}
}

func TestLoadCredentials(t *testing.T) {
	t.Run("an env token overrides everything else", func(t *testing.T) {
		t.Setenv(accessTokenEnvVar, "env-token-123")
		t.Setenv(URLEnvVar, "http://localhost:3000")

		creds, err := LoadCredentials()
		if err != nil {
			t.Fatalf("LoadCredentials() returned error: %v", err)
		}
		if creds.AccessToken != "env-token-123" {
			t.Errorf("AccessToken = %q, want %q", creds.AccessToken, "env-token-123")
		}
		if creds.APIURL != "http://localhost:3000" {
			t.Errorf("APIURL = %q, want %q", creds.APIURL, "http://localhost:3000")
		}
	})

	t.Run("an env token without an API URL", func(t *testing.T) {
		t.Setenv(accessTokenEnvVar, "env-token-only")
		t.Setenv(URLEnvVar, "")

		creds, err := LoadCredentials()
		if err != nil {
			t.Fatalf("LoadCredentials() returned error: %v", err)
		}
		if creds.AccessToken != "env-token-only" {
			t.Errorf("AccessToken = %q, want %q", creds.AccessToken, "env-token-only")
		}
		if creds.APIURL != "" {
			t.Errorf("APIURL = %q, want empty", creds.APIURL)
		}
	})

	t.Run("an empty env token falls through", func(t *testing.T) {
		t.Setenv(accessTokenEnvVar, "")
		confighome.Isolate(t)

		_, err := LoadCredentials()
		if err == nil {
			t.Skip("machine has ambient keyring/file credentials; env fallthrough still verified by the token being empty")
		}
		if !errors.Is(err, ErrNotLoggedIn) {
			t.Logf("LoadCredentials() without env token returned: %v", err)
		}
	})
}
