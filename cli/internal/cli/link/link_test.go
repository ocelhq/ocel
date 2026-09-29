package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/exitcode"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/runui"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

type cloudServer struct {
	*httptest.Server
	orgs           []map[string]string
	projects       []map[string]string
	created        []map[string]string
	setActive      int
	createConflict bool
	slow           time.Duration
}

func newCloudServer(t *testing.T, projects ...map[string]string) *cloudServer {
	t.Helper()
	c := &cloudServer{
		orgs:     []map[string]string{{"id": "org_1", "name": "Acme Inc", "slug": "acme-inc"}},
		projects: projects,
	}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/auth/organization/list":
			time.Sleep(c.slow)
			json.NewEncoder(w).Encode(c.orgs)
		case r.URL.Path == "/api/auth/organization/set-active":
			c.setActive++
			w.Write([]byte("{}"))
		case r.URL.Path == "/api/projects" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(c.projects)
		case r.URL.Path == "/api/projects" && r.Method == http.MethodPost:
			if c.createConflict {
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(map[string]string{"error": "taken"})
				return
			}
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			c.created = append(c.created, body)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{
				"id": "proj_new", "organizationId": "org_1",
				"name": body["name"], "slug": body["slug"],
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(c.Close)
	return c
}

func projectRow(id, name, slug string) map[string]string {
	return map[string]string{"id": id, "organizationId": "org_1", "name": name, "slug": slug}
}

func readLink(t *testing.T, dir, apiURL string) *console.Link {
	t.Helper()
	record, err := console.ReadLink(dir, apiURL)
	if err != nil {
		t.Fatalf("consolelink.Read: %v", err)
	}
	return record
}

func TestRunLink(t *testing.T) {
	t.Parallel()

	t.Run("not logged in returns an exit error pointing at `ocel login`", func(t *testing.T) {
		t.Parallel()

		deps := clitest.NewDeps()
		deps.LoadCredentials = func() (console.Credentials, error) {
			return console.Credentials{}, console.ErrNotLoggedIn
		}

		var stderr bytes.Buffer
		err := run(context.Background(), deps, t.TempDir(), "", options{}, &bytes.Buffer{}, &stderr, strings.NewReader(""))

		var exitErr *exitcode.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run err = %v (%T), want *exitcode.ExitError", err, err)
		}
		if !strings.Contains(stderr.String(), "ocel login") {
			t.Fatalf("stderr = %q, want it to mention `ocel login`", stderr.String())
		}
	})

	t.Run("it selects an existing project by slug", func(t *testing.T) {
		t.Parallel()

		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t, projectRow("p1", "My App", "my-app"), projectRow("p2", "Other", "other"))
		dir := t.TempDir()

		opts := options{apiURL: srv.URL}
		if err := run(context.Background(), deps, dir, "other", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader("")); err != nil {
			t.Fatalf("run err = %v", err)
		}

		record := readLink(t, dir, srv.URL)
		if record == nil {
			t.Fatal("no link written")
		}
		want := console.Link{APIURL: srv.URL, OrganizationID: "org_1", ProjectID: "p2", ProjectName: "Other"}
		if *record != want {
			t.Fatalf("link = %+v, want %+v", *record, want)
		}
		if len(srv.created) != 0 {
			t.Fatalf("created = %v, want no project creation", srv.created)
		}
		if srv.setActive != 1 {
			t.Fatalf("set-active calls = %d, want 1", srv.setActive)
		}
	})

	t.Run("an unknown slug errors listing the available projects", func(t *testing.T) {
		t.Parallel()

		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t, projectRow("p1", "My App", "my-app"))

		out := failedLink(t, deps, t.TempDir(), "nope", options{apiURL: srv.URL})
		if !strings.Contains(out, "my-app") {
			t.Fatalf("output = %q, want it to list the available slugs", out)
		}
	})

	t.Run("without a terminal and without a project or --create it errors about the flags", func(t *testing.T) {
		t.Parallel()

		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t, projectRow("p1", "My App", "my-app"))

		dir := t.TempDir()
		out := failedLink(t, deps, dir, "", options{apiURL: srv.URL})
		if !strings.Contains(out, "--create") {
			t.Fatalf("output = %q, want it to mention --create", out)
		}
		if readLink(t, dir, srv.URL) != nil {
			t.Fatal("a link was written despite the error")
		}
	})

	t.Run("--create without a name uses the directory name", func(t *testing.T) {
		t.Parallel()

		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t)

		dir := filepath.Join(t.TempDir(), "my-fresh-app")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		opts := options{apiURL: srv.URL, create: true}
		if err := run(context.Background(), deps, dir, "", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader("")); err != nil {
			t.Fatalf("run err = %v", err)
		}

		if len(srv.created) != 1 || srv.created[0]["slug"] != "my-fresh-app" {
			t.Fatalf("created = %v, want one project slugged after the directory", srv.created)
		}
		record := readLink(t, dir, srv.URL)
		if record == nil || record.ProjectID != "proj_new" {
			t.Fatalf("link = %+v, want the created project", record)
		}
	})

	t.Run("--create with a name slugifies it", func(t *testing.T) {
		t.Parallel()

		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t)

		opts := options{apiURL: srv.URL, create: true}
		if err := run(context.Background(), deps, t.TempDir(), "My Cool App", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader("")); err != nil {
			t.Fatalf("run err = %v", err)
		}
		if len(srv.created) != 1 || srv.created[0]["slug"] != "my-cool-app" || srv.created[0]["name"] != "My Cool App" {
			t.Fatalf("created = %v, want name/slug from the argument", srv.created)
		}
	})

	t.Run("a create conflict points at linking to the existing project", func(t *testing.T) {
		t.Parallel()

		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t)
		srv.createConflict = true

		out := failedLink(t, deps, t.TempDir(), "My App", options{apiURL: srv.URL, create: true})
		if !strings.Contains(out, "ocel link my-app") {
			t.Fatalf("output = %q, want it to suggest `ocel link my-app`", out)
		}
	})

	t.Run("several organizations without a terminal require --org", func(t *testing.T) {
		t.Parallel()

		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t)
		srv.orgs = append(srv.orgs, map[string]string{"id": "org_2", "name": "Other Co", "slug": "other-co"})

		out := failedLink(t, deps, t.TempDir(), "My App", options{apiURL: srv.URL, create: true})
		if !strings.Contains(out, "--org") {
			t.Fatalf("output = %q, want it to mention --org", out)
		}
	})

	t.Run("--org selects among several", func(t *testing.T) {
		t.Parallel()

		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t)
		srv.orgs = append(srv.orgs, map[string]string{"id": "org_2", "name": "Other Co", "slug": "other-co"})

		dir := t.TempDir()
		opts := options{apiURL: srv.URL, create: true, org: "other-co"}
		if err := run(context.Background(), deps, dir, "My App", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader("")); err != nil {
			t.Fatalf("run err = %v", err)
		}
		record := readLink(t, dir, srv.URL)
		if record == nil || record.OrganizationID != "org_2" {
			t.Fatalf("link = %+v, want organizationId org_2", record)
		}
	})

	t.Run("an unknown --org errors listing the available org slugs", func(t *testing.T) {
		t.Parallel()

		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t)

		out := failedLink(t, deps, t.TempDir(), "My App", options{apiURL: srv.URL, create: true, org: "nope"})
		if !strings.Contains(out, "acme-inc") {
			t.Fatalf("output = %q, want it to list the available org slugs", out)
		}
	})

	t.Run("relinking reports the previous link and replaces it", func(t *testing.T) {
		t.Parallel()

		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t, projectRow("p1", "My App", "my-app"), projectRow("p2", "Other", "other"))

		dir := t.TempDir()
		if err := console.WriteLink(dir, console.Link{
			APIURL: srv.URL, OrganizationID: "org_1", ProjectID: "p1", ProjectName: "My App",
		}); err != nil {
			t.Fatalf("seed link: %v", err)
		}

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		opts := options{apiURL: srv.URL}
		if err := run(context.Background(), deps, dir, "other", opts, &stdout, &bytes.Buffer{}, strings.NewReader("")); err != nil {
			t.Fatalf("run err = %v", err)
		}
		if !strings.Contains(stdout.String(), "INFO  [check] This directory is linked to My App now; linking it again\n") {
			t.Fatalf("stdout = %q, want it to report the previous link", stdout.String())
		}
		if record := readLink(t, dir, srv.URL); record == nil || record.ProjectID != "p2" {
			t.Fatalf("link = %+v, want it replaced with p2", record)
		}
	})

	t.Run("it ignores a record from another control plane", func(t *testing.T) {
		t.Parallel()

		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t, projectRow("p1", "My App", "my-app"))

		dir := t.TempDir()
		if err := console.WriteLink(dir, console.Link{
			APIURL: "https://elsewhere.example.com", OrganizationID: "org_9", ProjectID: "p9", ProjectName: "Elsewhere",
		}); err != nil {
			t.Fatalf("seed link: %v", err)
		}

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		opts := options{apiURL: srv.URL}
		if err := run(context.Background(), deps, dir, "my-app", opts, &stdout, &bytes.Buffer{}, strings.NewReader("")); err != nil {
			t.Fatalf("run err = %v", err)
		}
		if strings.Contains(stdout.String(), "Elsewhere") {
			t.Fatalf("stdout = %q, want no mention of the other control plane's link", stdout.String())
		}
		if record := readLink(t, dir, srv.URL); record == nil || record.ProjectID != "p1" {
			t.Fatalf("link = %+v, want the new control plane's project", record)
		}
		if record := readLink(t, dir, "https://elsewhere.example.com"); record != nil {
			t.Fatalf("link = %+v, want the old record replaced", record)
		}
	})
}

func TestLinkingShowsEachConsoleWaitAsAUnitOnItsRunAndNothingElseWritesTheTerminal(t *testing.T) {
	t.Parallel()

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	deps.Presentation = func(io.Writer) runui.Presentation {
		return runui.Resolve(runui.Origin{LogFormat: runui.FormatJSON, TTY: true, Width: 80})
	}
	var stdout safeBuffer
	clitest.AttachTerminalSink(deps, &stdout)
	srv := newCloudServer(t, projectRow("p1", "My App", "my-app"), projectRow("p2", "Other", "other"))
	srv.slow = 300 * time.Millisecond

	if err := run(context.Background(), deps, t.TempDir(), "other", options{apiURL: srv.URL}, &stdout, &bytes.Buffer{}, strings.NewReader("")); err != nil {
		t.Fatalf("run err = %v", err)
	}

	evs := runEvents(t, stdout.String())
	var units []string
	ended := map[string]bool{}
	for _, ev := range evs {
		switch {
		case ev.GetStarted() != nil && ev.GetMessage() != "":
			units = append(units, ev.GetSubject()+": "+ev.GetMessage())
		case ev.GetEnded() != nil:
			ended[string(ev.GetSpanId())] = true
		}
	}
	want := []string{"127.0.0.1: Loading your organizations", "acme-inc: Loading the projects in Acme Inc"}
	if !slices.Equal(units, want) {
		t.Fatalf("units = %q, want %q", units, want)
	}
	for _, ev := range evs {
		if ev.GetStarted() != nil && !ended[string(ev.GetSpanId())] {
			t.Errorf("scope %q never ended", ev.GetMessage())
		}
	}
	result := evs[len(evs)-1].GetResult()
	if !result.GetSuccess() || result.GetHeadline() != "Linked this directory to other (Acme Inc)" {
		t.Fatalf("result = %v, want the run to succeed saying what it linked", result)
	}
}

func failedLink(t *testing.T, deps cmddeps.Deps, dir, projectRef string, opts options) string {
	t.Helper()
	var out bytes.Buffer
	clitest.AttachTerminalSink(deps, &out)
	err := run(context.Background(), deps, dir, projectRef, opts, &out, &bytes.Buffer{}, strings.NewReader(""))
	var exitErr *exitcode.ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("run err = %v, want the run to fail with exit code 1", err)
	}
	if !strings.Contains(out.String(), "✗ Link failed") {
		t.Fatalf("output = %q, want the run's failure", out.String())
	}
	return out.String()
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func runEvents(t *testing.T, out string) []*streamv1.RunEvent {
	t.Helper()
	var evs []*streamv1.RunEvent
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		ev := &streamv1.RunEvent{}
		if err := protojson.Unmarshal([]byte(line), ev); err != nil {
			t.Fatalf("line %q is not a protojson RunEvent: %v", line, err)
		}
		evs = append(evs, ev)
	}
	if len(evs) == 0 {
		t.Fatal("the run drew nothing")
	}
	return evs
}

func TestRunUnlink(t *testing.T) {
	t.Parallel()

	t.Run("it removes the record", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		if err := console.WriteLink(dir, console.Link{APIURL: "https://ocel.app", ProjectID: "p1"}); err != nil {
			t.Fatalf("seed link: %v", err)
		}

		var stdout bytes.Buffer
		if err := runUnlink(dir, &stdout); err != nil {
			t.Fatalf("runUnlink err = %v", err)
		}
		if !strings.Contains(stdout.String(), "Unlinked") {
			t.Fatalf("stdout = %q, want it to confirm the unlink", stdout.String())
		}
		if record := readLink(t, dir, "https://ocel.app"); record != nil {
			t.Fatalf("link = %+v after unlink, want nil", record)
		}
	})

	t.Run("nothing to remove is not an error", func(t *testing.T) {
		t.Parallel()

		var stdout bytes.Buffer
		if err := runUnlink(t.TempDir(), &stdout); err != nil {
			t.Fatalf("runUnlink err = %v, want nil", err)
		}
		if !strings.Contains(stdout.String(), "isn't linked") {
			t.Fatalf("stdout = %q, want it to say the directory isn't linked", stdout.String())
		}
	})
}

func TestProjectDir(t *testing.T) {
	t.Parallel()

	t.Run("a subdirectory links the project root, where dev and run read the link", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "my-app", "provider": { "fake": {} }}`)
		nested := filepath.Join(root, "apps", "web")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		deps := clitest.NewDeps()
		deps.ConfigPath = func() string { return "" }
		got, err := projectDir(context.Background(), deps, nested)
		if err != nil {
			t.Fatalf("projectDir err = %v", err)
		}
		if got != root {
			t.Fatalf("projectDir = %q, want the project root %q", got, root)
		}
	})
}
