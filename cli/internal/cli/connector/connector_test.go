package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	consoleconnector "github.com/ocelhq/ocel/cli/internal/console/connector"
	"github.com/ocelhq/ocel/cli/internal/console/credentials"
	consolelink "github.com/ocelhq/ocel/cli/internal/console/link"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/providers"
	"github.com/ocelhq/ocel/pkg/constants"
)

const (
	fingerprint = "vps/sha256:aaaa/ocel"
	hostname    = "box.example.com"
)

type consoleServer struct {
	*httptest.Server
	rows        []map[string]any
	upserted    []map[string]any
	patched     []map[string]any
	deleted     []string
	refusePatch bool
}

func newConsoleServer(t *testing.T, rows ...map[string]any) *consoleServer {
	t.Helper()

	c := &consoleServer{rows: rows}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/connectors" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(c.rows)
		case r.URL.Path == "/api/connectors" && r.Method == http.MethodPut:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			c.upserted = append(c.upserted, body)
			registered := map[string]any{"id": "con_1", "organizationId": "org_1", "capabilities": []string{}}
			for key, value := range body {
				registered[key] = value
			}
			c.rows = append(c.rows, registered)
			_ = json.NewEncoder(w).Encode(registered)
		case strings.HasPrefix(r.URL.Path, "/api/connectors/") && r.Method == http.MethodPatch:
			if c.refusePatch {
				http.Error(w, "nope", http.StatusInternalServerError)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			c.patched = append(c.patched, body)
			registered := map[string]any{"id": "con_1", "target": fingerprint, "vendor": "vps", "compute": "container", "reach": "dial"}
			for key, value := range body {
				registered[key] = value
			}
			_ = json.NewEncoder(w).Encode(registered)
		case strings.HasPrefix(r.URL.Path, "/api/connectors/") && r.Method == http.MethodDelete:
			c.deleted = append(c.deleted, strings.TrimPrefix(r.URL.Path, "/api/connectors/"))
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(c.Close)
	return c
}

func linked(t *testing.T, dir, apiURL string) {
	t.Helper()

	if err := consolelink.Write(dir, consolelink.Link{
		APIURL:         apiURL,
		OrganizationID: "org_1",
		ProjectID:      "p1",
		ProjectName:    "My App",
	}); err != nil {
		t.Fatalf("consolelink.Write: %v", err)
	}
}

func resolved(t *testing.T, root string) *projectconfig.Config {
	t.Helper()

	cfg, err := projectconfig.Resolve(context.Background(), root, filepath.Join(root, "ocel.vps.json"))
	if err != nil {
		t.Fatalf("projectconfig.Resolve: %v", err)
	}
	return cfg
}

func opened(t *testing.T, srv *consoleServer) options {
	t.Helper()

	return options{write: true, apiURL: srv.URL, console: consoleconnector.New(srv.URL)}
}

func TestAddPairsTheTargetWithTheConsoleAndInstallsTheAsset(t *testing.T) {
	root := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	srv := newConsoleServer(t)
	linked(t, root, srv.URL)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	deps.ConfigPath = func() string { return filepath.Join(root, "ocel.vps.json") }

	var stdout, stderr bytes.Buffer
	if err := runAdd(context.Background(), deps, resolved(t, root), read(t, root, srv.URL), opened(t, srv), &stdout, &stderr); err != nil {
		t.Fatalf("runAdd err = %v\n%s", err, stderr.String())
	}

	if len(srv.upserted) != 1 {
		t.Fatalf("upserts = %v, want one", srv.upserted)
	}
	want := map[string]any{"target": fingerprint, "vendor": "vps", "reach": "dial"}
	for key, value := range want {
		if srv.upserted[0][key] != value {
			t.Errorf("upsert %s = %v, want %v", key, srv.upserted[0][key], value)
		}
	}
	if len(srv.patched) != 1 {
		t.Fatalf("patches = %v, want one", srv.patched)
	}
	if srv.patched[0]["url"] != "https://"+hostname+"/"+constants.ProjectStateDirName+"/connector" {
		t.Errorf("patched url = %v, want the box's own connector path", srv.patched[0]["url"])
	}
	if srv.patched[0]["publicKey"] == "" {
		t.Error("the console was told no public key, so it can verify no heartbeat")
	}
	if srv.patched[0]["compute"] != "container" {
		t.Errorf("patched compute = %v, want the compute the provider chose for itself", srv.patched[0]["compute"])
	}

	log, err := clitest.LoadFakeConnectorLog(os.Getenv(clitest.FakeConnectorLogEnvVar))
	if err != nil {
		t.Fatal(err)
	}
	if !log.Installed {
		t.Fatal("the provider was never asked to install a connector")
	}
	if string(log.Binary) != clitest.FakeConnectorBinary {
		t.Errorf("the provider was handed %q, want the connector asset the arch names", string(log.Binary))
	}
	var config map[string]any
	if err := json.Unmarshal(log.ConfigJSON, &config); err != nil {
		t.Fatalf("the config the provider was handed is not an object: %v", err)
	}
	if config["console"] != srv.URL || config["connectorId"] != "con_1" || config["organizationId"] != "org_1" {
		t.Errorf("config = %v, want it to name this console, the row it upserted and the org", config)
	}
	if config["target"] != fingerprint {
		t.Errorf("config target = %v, want %q", config["target"], fingerprint)
	}
	grants, _ := config["grants"].([]any)
	if len(grants) != 2 || grants[0] != "envvars.read" || grants[1] != "envvars.write" {
		t.Errorf("grants = %v, want read and write and no reveal", grants)
	}
	if !strings.Contains(stdout.String(), fingerprint) {
		t.Errorf("stdout = %q, want it to name the target it paired", stdout.String())
	}
}

func TestAddGrantsRevealOnlyWhenItIsAskedFor(t *testing.T) {
	root := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	srv := newConsoleServer(t)
	linked(t, root, srv.URL)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	opts := opened(t, srv)
	opts.reveal = true
	opts.write = false
	if err := runAdd(context.Background(), deps, resolved(t, root), read(t, root, srv.URL), opts, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runAdd err = %v", err)
	}

	log, err := clitest.LoadFakeConnectorLog(os.Getenv(clitest.FakeConnectorLogEnvVar))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(log.ConfigJSON, &config); err != nil {
		t.Fatal(err)
	}
	grants, _ := config["grants"].([]any)
	if len(grants) != 2 || grants[0] != "envvars.read" || grants[1] != "envvars.reveal" {
		t.Errorf("grants = %v, want read and reveal and no write", grants)
	}
}

func TestAConnectorOverTheChannelCeilingIsRefusedBeforeItIsSent(t *testing.T) {
	root := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	srv := newConsoleServer(t)
	linked(t, root, srv.URL)

	binary := clitest.InstallConnector(t, "vps", providers.Platform{GOOS: "linux", GOARCH: "amd64"}, []byte(clitest.FakeConnectorBinary))
	if err := os.Truncate(binary, providerclient.MaxMessageBytes+1); err != nil {
		t.Fatalf("grow the connector past the ceiling: %v", err)
	}

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	err := runAdd(context.Background(), deps, resolved(t, root), read(t, root, srv.URL),
		opened(t, srv), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("runAdd err = nil, want a connector over the ceiling refused")
	}
	said := err.Error()
	for _, want := range []string{"vps connector", "over the"} {
		if !strings.Contains(said, want) {
			t.Errorf("runAdd err = %q, want it to contain %q", said, want)
		}
	}
	if _, statErr := os.Stat(os.Getenv(clitest.FakeConnectorLogEnvVar)); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the connector was installed anyway (stat err = %v), want nothing sent over the channel", statErr)
	}
}

func TestAFailedAddSaysRunningItAgainFinishesIt(t *testing.T) {
	root := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	srv := newConsoleServer(t)
	srv.refusePatch = true
	linked(t, root, srv.URL)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	err := runAdd(context.Background(), deps, resolved(t, root), read(t, root, srv.URL),
		opened(t, srv), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("a console that refused the address answered no error")
	}
	said := err.Error()
	if !strings.Contains(said, "ocel connector add") || !strings.Contains(said, "already has this target registered") {
		t.Errorf("runAdd err = %q, and a half-finished add has to say that the console has the target registered and that re-running finishes it", said)
	}
	if len(srv.upserted) != 1 {
		t.Errorf("upserts = %v, want the one the run made before it failed", srv.upserted)
	}
}

func TestRemoveTakesTheConnectorOffTheBoxAndForgetsTheRow(t *testing.T) {
	root := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	srv := newConsoleServer(t, map[string]any{
		"id": "con_1", "target": fingerprint, "vendor": "vps", "compute": "container", "reach": "dial",
		"capabilities": []string{"envvars.read"},
	})
	linked(t, root, srv.URL)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout bytes.Buffer
	if err := runRemove(context.Background(), deps, resolved(t, root), read(t, root, srv.URL), opened(t, srv), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("runRemove err = %v", err)
	}

	log, err := clitest.LoadFakeConnectorLog(os.Getenv(clitest.FakeConnectorLogEnvVar))
	if err != nil {
		t.Fatal(err)
	}
	if !log.Removed {
		t.Fatal("the provider was never asked to take the connector off the machine")
	}
	if len(srv.deleted) != 1 || srv.deleted[0] != "con_1" {
		t.Fatalf("deleted = %v, want the one row keyed by this target", srv.deleted)
	}
}

func TestAnUnreachableMachineIsPointedAtRmTarget(t *testing.T) {
	root := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	t.Setenv(clitest.FakeConnectorRefuseEnvVar, "this machine is not reachable")
	srv := newConsoleServer(t, map[string]any{
		"id": "con_1", "target": fingerprint, "vendor": "vps", "compute": "container", "reach": "dial",
	})
	linked(t, root, srv.URL)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout bytes.Buffer
	err := runRemove(context.Background(), deps, resolved(t, root), read(t, root, srv.URL), opened(t, srv), &stdout, &bytes.Buffer{})
	if err == nil {
		t.Fatal("runRemove err = nil, want the unreachable machine reported")
	}
	said := err.Error()
	if !strings.Contains(said, "ocel connector status") || !strings.Contains(said, "rm --target") {
		t.Errorf("runRemove err = %q, want it to name the remedy for a target that will not answer", said)
	}
	if len(srv.deleted) != 0 {
		t.Fatalf("deleted = %v: with no fingerprint there is no row to key on", srv.deleted)
	}
}

func TestRmTargetForgetsTheRowWithoutTouchingTheTarget(t *testing.T) {
	root := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	t.Setenv(clitest.FakeConnectorRefuseEnvVar, "this machine is not reachable")
	srv := newConsoleServer(t, map[string]any{
		"id": "con_1", "target": fingerprint, "vendor": "vps", "compute": "container", "reach": "dial",
	})
	linked(t, root, srv.URL)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	opts := opened(t, srv)
	opts.target = fingerprint

	var stdout bytes.Buffer
	if err := runRemove(context.Background(), deps, resolved(t, root), read(t, root, srv.URL), opts, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("runRemove err = %v", err)
	}
	if len(srv.deleted) != 1 || srv.deleted[0] != "con_1" {
		t.Fatalf("deleted = %v, want the row keyed by the target the flag named", srv.deleted)
	}
	if log, err := clitest.LoadFakeConnectorLog(os.Getenv(clitest.FakeConnectorLogEnvVar)); err == nil && log.Removed {
		t.Error("the provider was asked to take the connector off a machine this run was told not to reach")
	}
	if !strings.Contains(stdout.String(), "never reached") {
		t.Errorf("stdout = %q, want it to say the target itself was left alone", stdout.String())
	}
}

func TestRmTargetSaysSoWhenTheConsoleHasNoSuchTarget(t *testing.T) {
	root := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	srv := newConsoleServer(t)
	linked(t, root, srv.URL)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	opts := opened(t, srv)
	opts.target = fingerprint

	var stdout bytes.Buffer
	if err := runRemove(context.Background(), deps, resolved(t, root), read(t, root, srv.URL), opts, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("runRemove err = %v", err)
	}
	if len(srv.deleted) != 0 {
		t.Fatalf("deleted = %v, want nothing deleted", srv.deleted)
	}
	if !strings.Contains(stdout.String(), "has no connector registered") {
		t.Errorf("stdout = %q, want it to say the console has no such target registered", stdout.String())
	}
}

func TestStatusSaysWhatTheConsoleHasRegistered(t *testing.T) {
	root := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	seen := time.Now().Add(-10 * time.Second)
	srv := newConsoleServer(t, map[string]any{
		"id": "con_1", "target": fingerprint, "vendor": "vps", "compute": "container", "reach": "dial",
		"url": "https://" + hostname + "/" + constants.ProjectStateDirName + "/connector", "capabilities": []string{"envvars.read", "envvars.write"},
		"connectedAt": seen, "lastSeenAt": seen, "online": true,
	})
	linked(t, root, srv.URL)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	deps.ConfigPath = func() string { return "" }

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), deps, resolved(t, root), read(t, root, srv.URL), opened(t, srv), &stdout); err != nil {
		t.Fatalf("runStatus err = %v", err)
	}
	for _, want := range []string{fingerprint, "container over dial", "online", "envvars.read, envvars.write"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
		}
	}
}

func TestStatusSaysSoWhenTheOrganizationHasNoConnector(t *testing.T) {
	root := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	srv := newConsoleServer(t)
	linked(t, root, srv.URL)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	deps.ConfigPath = func() string { return "" }

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), deps, resolved(t, root), read(t, root, srv.URL), opened(t, srv), &stdout); err != nil {
		t.Fatalf("runStatus err = %v", err)
	}
	if !strings.Contains(stdout.String(), "ocel connector add") {
		t.Errorf("stdout = %q, want it to point at the command that adds one", stdout.String())
	}
}

func TestAnUnlinkedTreeIsPointedAtOcelLink(t *testing.T) {
	root := clitest.SetUpConnectorFixture(t, fingerprint, hostname)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	deps.ConfigPath = func() string { return filepath.Join(root, "ocel.vps.json") }

	cmd := NewCommand(deps)
	cmd.SetArgs([]string{"status"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := chdir(t, root, cmd.Execute); !errors.As(err, new(*exitsig.ExitError)) {
		t.Fatalf("Execute() = %v, want an exit error", err)
	}
	if !strings.Contains(out.String(), "ocel link") {
		t.Errorf("output = %q, want it to point at `ocel link`", out.String())
	}
}

func TestBeingLoggedOutIsPointedAtOcelLogin(t *testing.T) {
	root := clitest.SetUpConnectorFixture(t, fingerprint, hostname)

	deps := clitest.NewDeps()
	deps.LoadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{}, credentials.ErrNotLoggedIn
	}
	deps.ConfigPath = func() string { return filepath.Join(root, "ocel.vps.json") }

	cmd := NewCommand(deps)
	cmd.SetArgs([]string{"status"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := chdir(t, root, cmd.Execute); !errors.As(err, new(*exitsig.ExitError)) {
		t.Fatalf("Execute() = %v, want an exit error", err)
	}
	if !strings.Contains(out.String(), "ocel login") {
		t.Errorf("output = %q, want it to point at `ocel login`", out.String())
	}
}

func TestTheVendorIsWhateverTheConfigPointsAtAndNoTableGatesIt(t *testing.T) {
	t.Parallel()

	for _, vendor := range []string{"aws", "gcp", "vps", "nowhere"} {
		cfg := &projectconfig.Config{
			Path:     "ocel." + vendor + ".json",
			Provider: &projectconfig.ProviderDescriptor{ID: vendor},
		}
		named, err := vendored(cfg)
		if err != nil {
			t.Fatalf("vendored(%s) = %v, want the vendor the config names", vendor, err)
		}
		if named != vendor {
			t.Errorf("vendored(%s) = %q, want %q", vendor, named, vendor)
		}
	}
}

func TestTheComputeGoesToTheProviderUntouched(t *testing.T) {
	root := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	srv := newConsoleServer(t)
	linked(t, root, srv.URL)

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	opts := opened(t, srv)
	opts.compute = "serverless"
	if err := runAdd(context.Background(), deps, resolved(t, root), read(t, root, srv.URL), opts, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runAdd err = %v", err)
	}

	log, err := clitest.LoadFakeConnectorLog(os.Getenv(clitest.FakeConnectorLogEnvVar))
	if err != nil {
		t.Fatal(err)
	}
	if log.Compute != "serverless" {
		t.Errorf("the provider was asked for %q, want the compute the flag named passed through with no vendor table in the way", log.Compute)
	}
	if len(srv.patched) != 1 || srv.patched[0]["compute"] != "serverless" {
		t.Errorf("patches = %v, want the console told what the provider chose", srv.patched)
	}
}

func read(t *testing.T, dir, apiURL string) *consolelink.Link {
	t.Helper()

	linked, err := consolelink.Read(dir, apiURL)
	if err != nil {
		t.Fatalf("consolelink.Read: %v", err)
	}
	if linked == nil {
		t.Fatal("no link written")
	}
	return linked
}

func chdir(t *testing.T, dir string, run func() error) error {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	return run()
}
