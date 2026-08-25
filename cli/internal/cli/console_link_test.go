package cli

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

	"github.com/ocelhq/ocel/cli/internal/consolebinding"
	"github.com/ocelhq/ocel/cli/internal/credentials"
	"github.com/ocelhq/ocel/cli/internal/exitsig"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

type cloudServer struct {
	*httptest.Server
	orgs           []map[string]string
	projects       []map[string]string
	created        []map[string]string
	setActive      int
	createConflict bool
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

func project(id, name, slug string) map[string]string {
	return map[string]string{"id": id, "organizationId": "org_1", "name": name, "slug": slug}
}

func readLink(t *testing.T, dir, apiURL string) *consolebinding.Binding {
	t.Helper()
	link, err := consolebinding.Read(dir, apiURL)
	if err != nil {
		t.Fatalf("consolebinding.Read: %v", err)
	}
	return link
}

func TestRunLink(t *testing.T) {
	t.Parallel()

	t.Run("not logged in returns an exit error pointing at `ocel login`", func(t *testing.T) {
		t.Parallel()

		deps := newDeps()
		deps.LoadCredentials = func() (credentials.Credentials, error) {
			return credentials.Credentials{}, credentials.ErrNotLoggedIn
		}

		var stderr bytes.Buffer
		err := runConsoleLink(context.Background(), deps, t.TempDir(), "", consoleLinkOptions{}, &bytes.Buffer{}, &stderr, strings.NewReader(""))

		var exitErr *exitsig.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("runConsoleLink err = %v (%T), want *exitsig.ExitError", err, err)
		}
		if !strings.Contains(stderr.String(), "ocel login") {
			t.Fatalf("stderr = %q, want it to mention `ocel login`", stderr.String())
		}
	})

	t.Run("it selects an existing project by slug", func(t *testing.T) {
		t.Parallel()

		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t, project("p1", "My App", "my-app"), project("p2", "Other", "other"))
		dir := t.TempDir()

		opts := consoleLinkOptions{apiURL: srv.URL}
		if err := runConsoleLink(context.Background(), deps, dir, "other", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader("")); err != nil {
			t.Fatalf("runConsoleLink err = %v", err)
		}

		link := readLink(t, dir, srv.URL)
		if link == nil {
			t.Fatal("no link written")
		}
		want := consolebinding.Binding{APIURL: srv.URL, OrganizationID: "org_1", ProjectID: "p2", ProjectName: "Other"}
		if *link != want {
			t.Fatalf("link = %+v, want %+v", *link, want)
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

		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t, project("p1", "My App", "my-app"))

		opts := consoleLinkOptions{apiURL: srv.URL}
		err := runConsoleLink(context.Background(), deps, t.TempDir(), "nope", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runConsoleLink err = nil, want error")
		}
		if !strings.Contains(err.Error(), "my-app") {
			t.Fatalf("err = %v, want it to list the available slugs", err)
		}
	})

	t.Run("without a terminal and without a project or --create it errors about the flags", func(t *testing.T) {
		t.Parallel()

		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t, project("p1", "My App", "my-app"))

		dir := t.TempDir()
		opts := consoleLinkOptions{apiURL: srv.URL}
		err := runConsoleLink(context.Background(), deps, dir, "", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runConsoleLink err = nil, want error")
		}
		if !strings.Contains(err.Error(), "--create") {
			t.Fatalf("err = %v, want it to mention --create", err)
		}
		if readLink(t, dir, srv.URL) != nil {
			t.Fatal("a link was written despite the error")
		}
	})

	t.Run("--create without a name uses the directory name", func(t *testing.T) {
		t.Parallel()

		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t)

		dir := filepath.Join(t.TempDir(), "my-fresh-app")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		opts := consoleLinkOptions{apiURL: srv.URL, create: true}
		if err := runConsoleLink(context.Background(), deps, dir, "", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader("")); err != nil {
			t.Fatalf("runConsoleLink err = %v", err)
		}

		if len(srv.created) != 1 || srv.created[0]["slug"] != "my-fresh-app" {
			t.Fatalf("created = %v, want one project slugged after the directory", srv.created)
		}
		link := readLink(t, dir, srv.URL)
		if link == nil || link.ProjectID != "proj_new" {
			t.Fatalf("link = %+v, want the created project", link)
		}
	})

	t.Run("--create with a name slugifies it", func(t *testing.T) {
		t.Parallel()

		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t)

		opts := consoleLinkOptions{apiURL: srv.URL, create: true}
		if err := runConsoleLink(context.Background(), deps, t.TempDir(), "My Cool App", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader("")); err != nil {
			t.Fatalf("runConsoleLink err = %v", err)
		}
		if len(srv.created) != 1 || srv.created[0]["slug"] != "my-cool-app" || srv.created[0]["name"] != "My Cool App" {
			t.Fatalf("created = %v, want name/slug from the argument", srv.created)
		}
	})

	t.Run("a create conflict points at linking to the existing project", func(t *testing.T) {
		t.Parallel()

		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t)
		srv.createConflict = true

		opts := consoleLinkOptions{apiURL: srv.URL, create: true}
		err := runConsoleLink(context.Background(), deps, t.TempDir(), "My App", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runConsoleLink err = nil, want error")
		}
		if !strings.Contains(err.Error(), "ocel console link my-app") {
			t.Fatalf("err = %v, want it to suggest `ocel console link my-app`", err)
		}
	})

	t.Run("several organizations without a terminal require --org", func(t *testing.T) {
		t.Parallel()

		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t)
		srv.orgs = append(srv.orgs, map[string]string{"id": "org_2", "name": "Other Co", "slug": "other-co"})

		opts := consoleLinkOptions{apiURL: srv.URL, create: true}
		err := runConsoleLink(context.Background(), deps, t.TempDir(), "My App", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil || !strings.Contains(err.Error(), "--org") {
			t.Fatalf("runConsoleLink err = %v, want it to mention --org", err)
		}
	})

	t.Run("--org selects among several", func(t *testing.T) {
		t.Parallel()

		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t)
		srv.orgs = append(srv.orgs, map[string]string{"id": "org_2", "name": "Other Co", "slug": "other-co"})

		dir := t.TempDir()
		opts := consoleLinkOptions{apiURL: srv.URL, create: true, org: "other-co"}
		if err := runConsoleLink(context.Background(), deps, dir, "My App", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader("")); err != nil {
			t.Fatalf("runConsoleLink err = %v", err)
		}
		link := readLink(t, dir, srv.URL)
		if link == nil || link.OrganizationID != "org_2" {
			t.Fatalf("link = %+v, want organizationId org_2", link)
		}
	})

	t.Run("an unknown --org errors listing the available org slugs", func(t *testing.T) {
		t.Parallel()

		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t)

		opts := consoleLinkOptions{apiURL: srv.URL, create: true, org: "nope"}
		err := runConsoleLink(context.Background(), deps, t.TempDir(), "My App", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil || !strings.Contains(err.Error(), "acme-inc") {
			t.Fatalf("runConsoleLink err = %v, want it to list the available org slugs", err)
		}
	})

	t.Run("relinking reports the previous link and replaces it", func(t *testing.T) {
		t.Parallel()

		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t, project("p1", "My App", "my-app"), project("p2", "Other", "other"))

		dir := t.TempDir()
		if err := consolebinding.Write(dir, consolebinding.Binding{
			APIURL: srv.URL, OrganizationID: "org_1", ProjectID: "p1", ProjectName: "My App",
		}); err != nil {
			t.Fatalf("seed link: %v", err)
		}

		var stdout bytes.Buffer
		opts := consoleLinkOptions{apiURL: srv.URL}
		if err := runConsoleLink(context.Background(), deps, dir, "other", opts, &stdout, &bytes.Buffer{}, strings.NewReader("")); err != nil {
			t.Fatalf("runConsoleLink err = %v", err)
		}
		if !strings.Contains(stdout.String(), "linked to My App") {
			t.Fatalf("stdout = %q, want it to report the previous link", stdout.String())
		}
		if link := readLink(t, dir, srv.URL); link == nil || link.ProjectID != "p2" {
			t.Fatalf("link = %+v, want it replaced with p2", link)
		}
	})

	t.Run("it ignores a record from another control plane", func(t *testing.T) {
		t.Parallel()

		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		srv := newCloudServer(t, project("p1", "My App", "my-app"))

		dir := t.TempDir()
		if err := consolebinding.Write(dir, consolebinding.Binding{
			APIURL: "https://elsewhere.example.com", OrganizationID: "org_9", ProjectID: "p9", ProjectName: "Elsewhere",
		}); err != nil {
			t.Fatalf("seed link: %v", err)
		}

		var stdout bytes.Buffer
		opts := consoleLinkOptions{apiURL: srv.URL}
		if err := runConsoleLink(context.Background(), deps, dir, "my-app", opts, &stdout, &bytes.Buffer{}, strings.NewReader("")); err != nil {
			t.Fatalf("runConsoleLink err = %v", err)
		}
		if strings.Contains(stdout.String(), "Elsewhere") {
			t.Fatalf("stdout = %q, want no mention of the other control plane's link", stdout.String())
		}
		if link := readLink(t, dir, srv.URL); link == nil || link.ProjectID != "p1" {
			t.Fatalf("link = %+v, want the new control plane's project", link)
		}
		if link := readLink(t, dir, "https://elsewhere.example.com"); link != nil {
			t.Fatalf("link = %+v, want the old record replaced", link)
		}
	})
}

func TestRunUnlink(t *testing.T) {
	t.Parallel()

	t.Run("it removes the record", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		if err := consolebinding.Write(dir, consolebinding.Binding{APIURL: "https://ocel.app", ProjectID: "p1"}); err != nil {
			t.Fatalf("seed link: %v", err)
		}

		var stdout bytes.Buffer
		if err := runConsoleUnlink(dir, &stdout); err != nil {
			t.Fatalf("runConsoleUnlink err = %v", err)
		}
		if !strings.Contains(stdout.String(), "Unlinked") {
			t.Fatalf("stdout = %q, want it to confirm the unlink", stdout.String())
		}
		if link := readLink(t, dir, "https://ocel.app"); link != nil {
			t.Fatalf("link = %+v after unlink, want nil", link)
		}
	})

	t.Run("nothing to remove is not an error", func(t *testing.T) {
		t.Parallel()

		var stdout bytes.Buffer
		if err := runConsoleUnlink(t.TempDir(), &stdout); err != nil {
			t.Fatalf("runConsoleUnlink err = %v, want nil", err)
		}
		if !strings.Contains(stdout.String(), "isn't linked") {
			t.Fatalf("stdout = %q, want it to say the directory isn't linked", stdout.String())
		}
	})
}
