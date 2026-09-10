package env

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/console/credentials"
	"github.com/ocelhq/ocel/cli/internal/console/envstore"
	"github.com/ocelhq/ocel/cli/internal/console/link"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

const devProjectID = "01a07490-a986-71d8-b40b-086ae98de225"

type fakeConsole struct {
	values map[string]string
	token  string
	paths  []string
}

func (c *fakeConsole) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	c.paths = append(c.paths, r.Method+" "+r.URL.Path)

	prefix := "/api/projects/" + devProjectID + "/env"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.Error(w, `{"error":"Not found"}`, http.StatusNotFound)
		return
	}
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, prefix), "/")

	switch r.Method {
	case http.MethodGet:
		if key != "" {
			held, ok := c.values[key]
			if !ok {
				http.Error(w, `{"error":"Not found"}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(envstore.Value{Key: key, Value: held, UpdatedAt: 1_700_000_000_000})
			return
		}
		keys := make([]string, 0, len(c.values))
		for k := range c.values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]envstore.Value, 0, len(keys))
		for _, k := range keys {
			out = append(out, envstore.Value{Key: k, Value: c.values[k], UpdatedAt: 1_700_000_000_000})
		}
		_ = json.NewEncoder(w).Encode(out)
	case http.MethodPut:
		var body struct {
			Value string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"Invalid request body"}`, http.StatusBadRequest)
			return
		}
		c.values[key] = body.Value
		_ = json.NewEncoder(w).Encode(map[string]any{"key": key, "size": len(body.Value)})
	case http.MethodDelete:
		_, held := c.values[key]
		delete(c.values, key)
		_ = json.NewEncoder(w).Encode(map[string]bool{"deleted": held})
	default:
		http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func setUpDevFixture(t *testing.T) (string, cmddeps.Deps, *fakeConsole) {
	t.Helper()

	root := setUpEnvFixture(t)
	console := &fakeConsole{values: map[string]string{}}
	server := httptest.NewServer(console)
	t.Cleanup(server.Close)
	t.Setenv("OCEL_API_URL", server.URL)

	if err := link.Write(root, link.Link{
		APIURL:         server.URL,
		OrganizationID: "org",
		ProjectID:      devProjectID,
		ProjectName:    "Journey",
	}); err != nil {
		t.Fatalf("write the console link: %v", err)
	}

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	return root, deps, console
}

func TestRunEnvDev(t *testing.T) {
	t.Run("a value set with --dev lands in the console, never in the provider", func(t *testing.T) {
		root, deps, console := setUpDevFixture(t)

		var stdout, stderr bytes.Buffer
		if err := runEnvSet(context.Background(), deps, root, "LOG_LEVEL", "debug", envOptions{dev: true}, nil, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvSet --dev err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if console.values["LOG_LEVEL"] != "debug" {
			t.Errorf("the console holds %v, want LOG_LEVEL=debug", console.values)
		}
		if console.token != "tok" {
			t.Errorf("the console saw token %q, want the logged-in access token", console.token)
		}

		t.Run("get --dev withholds the value until --reveal", func(t *testing.T) {
			console.paths = nil
			var plain bytes.Buffer
			if err := runEnvGet(context.Background(), deps, root, "LOG_LEVEL", envOptions{dev: true}, &plain, &plain); err != nil {
				t.Fatalf("runEnvGet --dev err = %v; out=%s", err, plain.String())
			}
			if strings.Contains(plain.String(), "debug") {
				t.Errorf("get --dev stdout = %q, want the value withheld without --reveal", plain.String())
			}

			var revealed bytes.Buffer
			if err := runEnvGet(context.Background(), deps, root, "LOG_LEVEL", envOptions{dev: true, reveal: true}, &revealed, &revealed); err != nil {
				t.Fatalf("runEnvGet --dev --reveal err = %v; out=%s", err, revealed.String())
			}
			if got := strings.TrimSpace(revealed.String()); got != "debug" {
				t.Errorf("get --dev --reveal stdout = %q, want exactly the value", got)
			}

			want := "GET /api/projects/" + devProjectID + "/env/LOG_LEVEL"
			for _, path := range console.paths {
				if path != want {
					t.Errorf("get --dev reached %q, want the key addressed directly at %q", path, want)
				}
			}
		})

		t.Run("ls --dev names the key it holds", func(t *testing.T) {
			var out bytes.Buffer
			if err := runEnvLs(context.Background(), deps, root, envOptions{dev: true}, &out, &out); err != nil {
				t.Fatalf("runEnvLs --dev err = %v; out=%s", err, out.String())
			}
			if !strings.Contains(out.String(), "LOG_LEVEL") {
				t.Errorf("ls --dev stdout = %q, want it to name LOG_LEVEL", out.String())
			}
			if strings.Contains(out.String(), "debug") {
				t.Errorf("ls --dev stdout = %q, want the value withheld", out.String())
			}
		})

		t.Run("rm --dev takes it back out", func(t *testing.T) {
			var out bytes.Buffer
			if err := runEnvRm(context.Background(), deps, root, "LOG_LEVEL", envOptions{dev: true}, &out, &out); err != nil {
				t.Fatalf("runEnvRm --dev err = %v; out=%s", err, out.String())
			}
			if _, held := console.values["LOG_LEVEL"]; held {
				t.Errorf("the console still holds %v after rm --dev", console.values)
			}
		})
	})

	t.Run("refuses a key no app declares, and the console holds nothing", func(t *testing.T) {
		root, deps, console := setUpDevFixture(t)

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), deps, root, "SITE_HOSTNAME", "acme.example", envOptions{dev: true}, nil, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet --dev SITE_HOSTNAME err = nil, want the gate to refuse a key nothing declares")
		}
		if !strings.Contains(err.Error(), "SITE_HOSTNAME") || !strings.Contains(err.Error(), "defineEnv") {
			t.Errorf("err = %v, want it to name the key and how to declare it", err)
		}
		if len(console.values) != 0 {
			t.Errorf("the console holds %v, want the refused write not to land", console.values)
		}
	})

	t.Run("refuses to write when nothing is linked, rather than writing .env behind the developer", func(t *testing.T) {
		root := setUpEnvFixture(t)
		t.Setenv("OCEL_API_URL", "http://127.0.0.1:1")
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), deps, root, "LOG_LEVEL", "debug", envOptions{dev: true}, nil, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet --dev unlinked err = nil, want a refusal")
		}
		want := "this project is not linked to a console, so nothing holds LOG_LEVEL. For `ocel dev`, put LOG_LEVEL=<VALUE> in .env; to share values with your team, run `ocel link`."
		if err.Error() != want {
			t.Errorf("err = %q, want %q", err.Error(), want)
		}
	})

	t.Run("refuses a key nothing holds without listing the store", func(t *testing.T) {
		root, deps, console := setUpDevFixture(t)
		console.paths = nil

		var out bytes.Buffer
		err := runEnvGet(context.Background(), deps, root, "LOG_LEVEL", envOptions{dev: true}, &out, &out)
		if err == nil {
			t.Fatal("runEnvGet --dev err = nil, want a refusal naming the key")
		}
		if !strings.Contains(err.Error(), "LOG_LEVEL") {
			t.Errorf("err = %v, want it to name the key", err)
		}
		for _, path := range console.paths {
			if !strings.HasSuffix(path, "/LOG_LEVEL") {
				t.Errorf("get --dev reached %q, want the key addressed directly", path)
			}
		}
	})

	t.Run("linked but logged out, it says to log in rather than that nothing is linked", func(t *testing.T) {
		root, deps, _ := setUpDevFixture(t)
		deps.LoadCredentials = func() (credentials.Credentials, error) {
			return credentials.Credentials{}, credentials.ErrNotLoggedIn
		}

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), deps, root, "LOG_LEVEL", "debug", envOptions{dev: true}, nil, &stdout, &stderr)
		var exit *exitsig.ExitError
		if !errors.As(err, &exit) || exit.Code != 1 {
			t.Fatalf("runEnvSet --dev logged out err = %v, want exit code 1", err)
		}
		if got := strings.TrimSpace(stderr.String()); got != "You're not logged in. Run `ocel login` first." {
			t.Errorf("stderr = %q, want the same refusal `ocel dev` prints", got)
		}
	})

	t.Run("refuses --dev alongside an environment that is not dev", func(t *testing.T) {
		root, deps, _ := setUpDevFixture(t)

		for name, opts := range map[string]envOptions{
			"--preview":     {dev: true, preview: true},
			"--environment": {dev: true, environment: "staging"},
			"--folder":      {dev: true, folder: "/web"},
		} {
			t.Run(name, func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				if err := runEnvSet(context.Background(), deps, root, "LOG_LEVEL", "debug", opts, nil, &stdout, &stderr); err == nil {
					t.Fatalf("runEnvSet --dev %s err = nil, want the two refused together", name)
				}
			})
		}
	})
}

func TestCheckDevWritable(t *testing.T) {
	t.Parallel()

	scoped := []*resourcesv1.VariableDefinition{
		{Key: "API_URL", Folders: []string{"web", "api"}},
		{Key: "LOG_LEVEL"},
	}

	t.Run("a folder-scoped key names .env, never --folder, which --dev refuses", func(t *testing.T) {
		t.Parallel()

		err := checkDevWritable(scoped, "API_URL")
		if err == nil {
			t.Fatal("checkDevWritable(API_URL) = nil, want a refusal")
		}
		if strings.Contains(err.Error(), "--folder") {
			t.Errorf("err = %v, want it not to name a flag --dev refuses", err)
		}
		if !strings.Contains(err.Error(), ".env") {
			t.Errorf("err = %v, want it to name .env as the way to hold a per-folder value", err)
		}
	})

	t.Run("an unscoped key is writable", func(t *testing.T) {
		t.Parallel()

		if err := checkDevWritable(scoped, "LOG_LEVEL"); err != nil {
			t.Fatalf("checkDevWritable(LOG_LEVEL) = %v, want nil", err)
		}
	})

	t.Run("a key nothing declares is refused with how to declare it", func(t *testing.T) {
		t.Parallel()

		err := checkDevWritable(scoped, "SITE_HOSTNAME")
		if err == nil || !strings.Contains(err.Error(), "defineEnv") {
			t.Fatalf("checkDevWritable(SITE_HOSTNAME) = %v, want the declaration gate's refusal", err)
		}
	})
}
