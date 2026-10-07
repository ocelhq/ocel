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

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/exitcode"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

type cloudServer struct {
	*httptest.Server
	consolev1connect.UnimplementedProjectServiceHandler
	orgs           []map[string]string
	projects       []*consolev1.Project
	created        []*consolev1.CreateProjectRequest
	setActive      int
	createConflict bool
	slow           time.Duration
}

func newCloudServer(t *testing.T, projects ...*consolev1.Project) *cloudServer {
	t.Helper()
	c := &cloudServer{
		orgs:     []map[string]string{{"id": "org_1", "name": "Acme Inc", "slug": "acme-inc"}},
		projects: projects,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/organization/list", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(c.slow)
		json.NewEncoder(w).Encode(c.orgs)
	})
	mux.HandleFunc("/api/auth/organization/set-active", func(w http.ResponseWriter, r *http.Request) {
		c.setActive++
		w.Write([]byte("{}"))
	})
	path, handler := consolev1connect.NewProjectServiceHandler(c, connect.WithInterceptors(connectvalidate.NewInterceptor()))
	mux.Handle("/api/connect"+path, http.StripPrefix("/api/connect", handler))
	c.Server = httptest.NewServer(mux)
	t.Cleanup(c.Close)
	return c
}

func (c *cloudServer) List(context.Context, *consolev1.ListProjectsRequest) (*consolev1.ListProjectsResponse, error) {
	return &consolev1.ListProjectsResponse{Projects: c.projects}, nil
}

func (c *cloudServer) Create(_ context.Context, req *consolev1.CreateProjectRequest) (*consolev1.CreateProjectResponse, error) {
	if c.createConflict {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("taken"))
	}
	c.created = append(c.created, req)
	return &consolev1.CreateProjectResponse{Project: &consolev1.Project{
		Id: "proj_new", OrganizationId: "org_1", Name: req.GetName(), Slug: req.GetSlug(),
	}}, nil
}

func projectRow(id, name, slug string) *consolev1.Project {
	return &consolev1.Project{Id: id, OrganizationId: "org_1", Name: name, Slug: slug}
}

func readLink(t *testing.T, dir, apiURL string) *console.Link {
	t.Helper()
	record, err := console.ReadLink(dir, apiURL)
	if err != nil {
		t.Fatalf("consolelink.Read: %v", err)
	}
	return record
}

func TestLinkSelectsOrCreatesAConsoleProjectForThisDirectory(t *testing.T) {
	t.Parallel()

	t.Run("not logged in returns an exit error pointing at `ocel login`", func(t *testing.T) {
		t.Parallel()

		dependencies := newTestDependencies()
		dependencies.LoadCredentials = func() (console.Credentials, error) {
			return console.Credentials{}, console.ErrNotLoggedIn
		}

		var stderr bytes.Buffer
		err := runLink(context.Background(), dependencies, t.TempDir(), "", options{}, &bytes.Buffer{}, &stderr, strings.NewReader(""))

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

		dependencies := newTestDependencies()
		dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
		srv := newCloudServer(t, projectRow("p1", "My App", "my-app"), projectRow("p2", "Other", "other"))
		dir := t.TempDir()

		opts := options{apiURL: srv.URL}
		if err := runLink(context.Background(), dependencies, dir, "other", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader("")); err != nil {
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

		dependencies := newTestDependencies()
		dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
		srv := newCloudServer(t, projectRow("p1", "My App", "my-app"))

		out := failedLink(t, dependencies, t.TempDir(), "nope", options{apiURL: srv.URL})
		if !strings.Contains(out, "my-app") {
			t.Fatalf("output = %q, want it to list the available slugs", out)
		}
	})

	t.Run("without a terminal and without a project or --create it errors about the flags", func(t *testing.T) {
		t.Parallel()

		dependencies := newTestDependencies()
		dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
		srv := newCloudServer(t, projectRow("p1", "My App", "my-app"))

		dir := t.TempDir()
		out := failedLink(t, dependencies, dir, "", options{apiURL: srv.URL})
		if !strings.Contains(out, "--create") {
			t.Fatalf("output = %q, want it to mention --create", out)
		}
		if readLink(t, dir, srv.URL) != nil {
			t.Fatal("a link was written despite the error")
		}
	})

	t.Run("--create without a name uses the directory name", func(t *testing.T) {
		t.Parallel()

		dependencies := newTestDependencies()
		dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
		srv := newCloudServer(t)

		dir := filepath.Join(t.TempDir(), "my-fresh-app")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		opts := options{apiURL: srv.URL, create: true}
		if err := runLink(context.Background(), dependencies, dir, "", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader("")); err != nil {
			t.Fatalf("run err = %v", err)
		}

		if len(srv.created) != 1 || srv.created[0].GetSlug() != "my-fresh-app" {
			t.Fatalf("created = %v, want one project slugged after the directory", srv.created)
		}
		record := readLink(t, dir, srv.URL)
		if record == nil || record.ProjectID != "proj_new" {
			t.Fatalf("link = %+v, want the created project", record)
		}
	})

	t.Run("--create with a name slugifies it", func(t *testing.T) {
		t.Parallel()

		dependencies := newTestDependencies()
		dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
		srv := newCloudServer(t)

		opts := options{apiURL: srv.URL, create: true}
		if err := runLink(context.Background(), dependencies, t.TempDir(), "My Cool App", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader("")); err != nil {
			t.Fatalf("run err = %v", err)
		}
		if len(srv.created) != 1 || srv.created[0].GetSlug() != "my-cool-app" || srv.created[0].GetName() != "My Cool App" {
			t.Fatalf("created = %v, want name/slug from the argument", srv.created)
		}
	})

	t.Run("a create conflict points at linking to the existing project", func(t *testing.T) {
		t.Parallel()

		dependencies := newTestDependencies()
		dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
		srv := newCloudServer(t)
		srv.createConflict = true

		out := failedLink(t, dependencies, t.TempDir(), "My App", options{apiURL: srv.URL, create: true})
		if !strings.Contains(out, "ocel link my-app") {
			t.Fatalf("output = %q, want it to suggest `ocel link my-app`", out)
		}
	})

	t.Run("several organizations without a terminal require --org", func(t *testing.T) {
		t.Parallel()

		dependencies := newTestDependencies()
		dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
		srv := newCloudServer(t)
		srv.orgs = append(srv.orgs, map[string]string{"id": "org_2", "name": "Other Co", "slug": "other-co"})

		out := failedLink(t, dependencies, t.TempDir(), "My App", options{apiURL: srv.URL, create: true})
		if !strings.Contains(out, "--org") {
			t.Fatalf("output = %q, want it to mention --org", out)
		}
	})

	t.Run("several organizations without a terminal end the JSON stream with input_required naming --org", func(t *testing.T) {
		t.Parallel()

		dependencies := newTestDependencies()
		dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
		srv := newCloudServer(t)
		srv.orgs = append(srv.orgs, map[string]string{"id": "org_2", "name": "Other Co", "slug": "other-co"})
		var stream bytes.Buffer
		dependencies.Events.Attach(terminal.NewJSONLines(&stream))

		_ = runLink(context.Background(), dependencies, t.TempDir(), "My App", options{apiURL: srv.URL, create: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))

		evs := clitest.RunEvents(t, stream.String())
		failure := evs[len(evs)-1].GetSummary().GetError()
		if failure.GetCode() != clierror.CodeInputRequired || failure.GetHint() != "--org <slug>" {
			t.Errorf("summary error = %v, want input_required with the hint --org <slug>", failure)
		}
	})

	for _, tc := range []struct {
		name     string
		projects []*consolev1.Project
		hint     string
	}{
		{"an organization with projects", []*consolev1.Project{projectRow("p1", "My App", "my-app")}, "ocel link <project>"},
		{"an organization with no projects", nil, "--create"},
	} {
		t.Run("no project chosen without a terminal in "+tc.name+" ends the JSON stream with input_required naming "+tc.hint, func(t *testing.T) {
			t.Parallel()

			dependencies := newTestDependencies()
			dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
			srv := newCloudServer(t, tc.projects...)
			var stream bytes.Buffer
			dependencies.Events.Attach(terminal.NewJSONLines(&stream))

			_ = runLink(context.Background(), dependencies, t.TempDir(), "", options{apiURL: srv.URL}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))

			evs := clitest.RunEvents(t, stream.String())
			failure := evs[len(evs)-1].GetSummary().GetError()
			if failure.GetCode() != clierror.CodeInputRequired || failure.GetHint() != tc.hint {
				t.Errorf("summary error = %v, want input_required with the hint %s", failure, tc.hint)
			}
		})
	}

	for _, tc := range []struct {
		name     string
		orgs     int
		projects []*consolev1.Project
		opts     options
		hint     string
		lists    []string
	}{
		{"several organizations", 2, nil, options{create: true}, "--org <slug>", []string{"acme-inc", "other-co"}},
		{"an organization with projects", 1, []*consolev1.Project{projectRow("p1", "My App", "my-app"), projectRow("p2", "Shop", "shop")}, options{}, "ocel link <project>", []string{"my-app", "shop"}},
		{"an organization with no projects", 1, nil, options{}, "--create", nil},
	} {
		t.Run("under --json a terminal is not asked about "+tc.name+" and input_required names "+tc.hint, func(t *testing.T) {
			t.Parallel()

			dependencies := newTestDependencies()
			dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
			tty, screen := clitest.UnderJSONOnATerminal(t, &dependencies.Invocation)
			srv := newCloudServer(t, tc.projects...)
			if tc.orgs > 1 {
				srv.orgs = append(srv.orgs, map[string]string{"id": "org_2", "name": "Other Co", "slug": "other-co"})
			}
			var stream bytes.Buffer
			dependencies.Events.Attach(terminal.NewJSONLines(&stream))
			opts := tc.opts
			opts.apiURL = srv.URL

			_ = clitest.FinishWithin(t, 20*time.Second, func(ctx context.Context) error {
				return runLink(ctx, dependencies, t.TempDir(), "", opts, &stream, &bytes.Buffer{}, tty)
			})

			if asked := screen(); asked != "" {
				t.Errorf("terminal = %q, want nothing asked under --json", asked)
			}
			evs := clitest.RunEvents(t, stream.String())
			failure := evs[len(evs)-1].GetSummary().GetError()
			if failure.GetCode() != clierror.CodeInputRequired || failure.GetHint() != tc.hint {
				t.Errorf("summary error = %v, want input_required with the hint %s", failure, tc.hint)
			}
			for _, choice := range tc.lists {
				if !strings.Contains(failure.GetMessage(), choice) {
					t.Errorf("message = %q, want it to list the choice %s", failure.GetMessage(), choice)
				}
			}
		})
	}

	t.Run("--org selects among several", func(t *testing.T) {
		t.Parallel()

		dependencies := newTestDependencies()
		dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
		srv := newCloudServer(t)
		srv.orgs = append(srv.orgs, map[string]string{"id": "org_2", "name": "Other Co", "slug": "other-co"})

		dir := t.TempDir()
		opts := options{apiURL: srv.URL, create: true, org: "other-co"}
		if err := runLink(context.Background(), dependencies, dir, "My App", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader("")); err != nil {
			t.Fatalf("run err = %v", err)
		}
		record := readLink(t, dir, srv.URL)
		if record == nil || record.OrganizationID != "org_2" {
			t.Fatalf("link = %+v, want organizationId org_2", record)
		}
	})

	t.Run("an unknown --org errors listing the available org slugs", func(t *testing.T) {
		t.Parallel()

		dependencies := newTestDependencies()
		dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
		srv := newCloudServer(t)

		out := failedLink(t, dependencies, t.TempDir(), "My App", options{apiURL: srv.URL, create: true, org: "nope"})
		if !strings.Contains(out, "acme-inc") {
			t.Fatalf("output = %q, want it to list the available org slugs", out)
		}
	})

	t.Run("relinking reports the previous link and replaces it", func(t *testing.T) {
		t.Parallel()

		dependencies := newTestDependencies()
		dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
		srv := newCloudServer(t, projectRow("p1", "My App", "my-app"), projectRow("p2", "Other", "other"))

		dir := t.TempDir()
		if err := console.WriteLink(dir, console.Link{
			APIURL: srv.URL, OrganizationID: "org_1", ProjectID: "p1", ProjectName: "My App",
		}); err != nil {
			t.Fatalf("seed link: %v", err)
		}

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		opts := options{apiURL: srv.URL}
		if err := runLink(context.Background(), dependencies, dir, "other", opts, &stdout, &bytes.Buffer{}, strings.NewReader("")); err != nil {
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

		dependencies := newTestDependencies()
		dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
		srv := newCloudServer(t, projectRow("p1", "My App", "my-app"))

		dir := t.TempDir()
		if err := console.WriteLink(dir, console.Link{
			APIURL: "https://elsewhere.example.com", OrganizationID: "org_9", ProjectID: "p9", ProjectName: "Elsewhere",
		}); err != nil {
			t.Fatalf("seed link: %v", err)
		}

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		opts := options{apiURL: srv.URL}
		if err := runLink(context.Background(), dependencies, dir, "my-app", opts, &stdout, &bytes.Buffer{}, strings.NewReader("")); err != nil {
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

func TestLinkingShowsEachConsoleWaitAsASpanOnItsRunAndNothingElseWritesTheTerminal(t *testing.T) {
	t.Parallel()

	dependencies := newTestDependencies()
	dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
	dependencies.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{Format: terminal.FormatJSON, TTY: true, Width: 80})
	}
	var stdout, stream safeBuffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	srv := newCloudServer(t, projectRow("p1", "My App", "my-app"), projectRow("p2", "Other", "other"))
	srv.slow = 300 * time.Millisecond

	if err := runLink(context.Background(), dependencies, t.TempDir(), "other", options{apiURL: srv.URL}, &stdout, &bytes.Buffer{}, strings.NewReader("")); err != nil {
		t.Fatalf("run err = %v", err)
	}

	evs := runEvents(t, stream.String())
	var spans []string
	ended := map[string]bool{}
	for _, ev := range evs {
		switch {
		case ev.GetOperation().GetStarted() != nil && ev.GetOperation().GetMessage() != "":
			spans = append(spans, ev.GetOperation().GetSubject()+": "+ev.GetOperation().GetMessage())
		case ev.GetOperation().GetEnded() != nil:
			ended[string(ev.GetOperation().GetSpanId())] = true
		}
	}
	want := []string{"127.0.0.1: Loading your organizations", "acme-inc: Loading the projects in Acme Inc"}
	if !slices.Equal(spans, want) {
		t.Fatalf("spans = %q, want %q", spans, want)
	}
	for _, ev := range evs {
		if ev.GetOperation().GetStarted() != nil && !ended[string(ev.GetOperation().GetSpanId())] {
			t.Errorf("scope %q never ended", ev.GetOperation().GetMessage())
		}
	}
	result := evs[len(evs)-1].GetSummary()
	if !result.GetSuccess() || result.GetHeadline() != "Linked this directory to other (Acme Inc)" {
		t.Fatalf("result = %v, want the run to succeed saying what it linked", result)
	}
}

func failedLink(t *testing.T, dependencies Dependencies, dir, projectRef string, opts options) string {
	t.Helper()
	var out bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &out)
	err := runLink(context.Background(), dependencies, dir, projectRef, opts, &out, &bytes.Buffer{}, strings.NewReader(""))
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

func TestUnlinkRemovesTheLinkRecord(t *testing.T) {
	t.Parallel()

	t.Run("it removes the record", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		if err := console.WriteLink(dir, console.Link{APIURL: "https://ocel.app", ProjectID: "p1"}); err != nil {
			t.Fatalf("seed link: %v", err)
		}

		var stdout bytes.Buffer
		if err := runUnlink(newTestDependencies(), dir, &stdout); err != nil {
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
		if err := runUnlink(newTestDependencies(), t.TempDir(), &stdout); err != nil {
			t.Fatalf("runUnlink err = %v, want nil", err)
		}
		if !strings.Contains(stdout.String(), "isn't linked") {
			t.Fatalf("stdout = %q, want it to say the directory isn't linked", stdout.String())
		}
	})
}

func TestASubdirectoryLinksTheProjectRoot(t *testing.T) {
	t.Parallel()

	t.Run("a subdirectory links the project root, where dev and run read the link", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "my-app", "provider": { "fake": {} }}`)
		nested := filepath.Join(root, "apps", "web")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		dependencies := newTestDependencies()
		dependencies.ConfigPath = func() string { return "" }
		got, err := projectDir(context.Background(), dependencies, nested)
		if err != nil {
			t.Fatalf("projectDir err = %v", err)
		}
		if got != root {
			t.Fatalf("projectDir = %q, want the project root %q", got, root)
		}
	})
}

func TestLinkAsJSONPrintsTheOrganizationAndProjectItLinked(t *testing.T) {
	t.Parallel()

	dependencies := newTestDependencies()
	dependencies.Presentation = clitest.ResolveJSONPresentation
	dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
	srv := newCloudServer(t, projectRow("p1", "My App", "my-app"), projectRow("p2", "Other", "other"))

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stderr)
	if err := runLink(context.Background(), dependencies, t.TempDir(), "other", options{apiURL: srv.URL}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runLink err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	var got resultv1.LinkResult
	clitest.DecodeResultInto(t, stdout.String(), &got)
	if org := got.GetOrganization(); org.GetId() != "org_1" || org.GetName() != "Acme Inc" || org.GetSlug() != "acme-inc" {
		t.Errorf("organization = %v, want the one linked through", org)
	}
	if project := got.GetProject(); project.GetId() != "p2" || project.GetName() != "Other" || project.GetSlug() != "other" {
		t.Errorf("project = %v, want the one selected", project)
	}
	if len(runEvents(t, stderr.String())) == 0 {
		t.Errorf("stream = %q, want the run's events there", stderr.String())
	}
}

func TestLinkAsJSONOfACreatedProjectPrintsTheNewProject(t *testing.T) {
	t.Parallel()

	dependencies := newTestDependencies()
	dependencies.Presentation = clitest.ResolveJSONPresentation
	dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
	srv := newCloudServer(t)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stderr)
	if err := runLink(context.Background(), dependencies, t.TempDir(), "My Cool App", options{apiURL: srv.URL, create: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runLink err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	var got resultv1.LinkResult
	clitest.DecodeResultInto(t, stdout.String(), &got)
	if project := got.GetProject(); project.GetId() != "proj_new" || project.GetName() != "My Cool App" || project.GetSlug() != "my-cool-app" {
		t.Errorf("project = %v, want the one created", project)
	}
}

func TestUnlinkAsJSONSaysWhetherALinkWasRemoved(t *testing.T) {
	t.Parallel()

	dependencies := newTestDependencies()
	dependencies.Presentation = clitest.ResolveJSONPresentation
	dir := t.TempDir()
	if err := console.WriteLink(dir, console.Link{APIURL: "https://ocel.app", ProjectID: "p1"}); err != nil {
		t.Fatalf("seed link: %v", err)
	}

	for i, want := range []bool{true, false} {
		var stdout bytes.Buffer
		if err := runUnlink(dependencies, dir, &stdout); err != nil {
			t.Fatalf("runUnlink err = %v", err)
		}
		var got resultv1.UnlinkResult
		clitest.DecodeResultInto(t, stdout.String(), &got)
		if got.GetUnlinked() != want {
			t.Errorf("unlink %d: unlinked = %v, want %v", i+1, got.GetUnlinked(), want)
		}
	}
}

func TestLinkAsksItsQuestionsWhereItsRunRendersAndLeavesStdoutEmpty(t *testing.T) {
	dependencies := newTestDependencies()
	dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
	dependencies.StdinIsTerminal = func(io.Reader) bool { return true }
	srv := newCloudServer(t, projectRow("p1", "My App", "my-app"))
	srv.orgs = append(srv.orgs, map[string]string{"id": "org_2", "name": "Other Co", "slug": "other-co"})

	var stdout, stderr safeBuffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stderr)
	if err := runLink(context.Background(), dependencies, t.TempDir(), "", options{apiURL: srv.URL}, &stdout, &stderr, strings.NewReader("acme-inc\n1\n")); err != nil {
		t.Fatalf("runLink err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want nothing there in human mode", stdout.String())
	}
	for _, question := range []string{"Select an organization (number or slug): ", "Select a project (number, slug, or n): "} {
		if !strings.Contains(stderr.String(), question) {
			t.Errorf("stderr = %q, want it to ask %q", stderr.String(), question)
		}
	}
}
