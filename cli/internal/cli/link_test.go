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

	"github.com/ocelhq/ocel/cli/internal/cloudlink"
	"github.com/ocelhq/ocel/cli/internal/credentials"
)

// cloudServer stands in for the control plane: one organization, the projects
// given, and project creation. It records what the CLI asked of it.
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

func readLink(t *testing.T, dir, apiURL string) *cloudlink.Link {
	t.Helper()
	link, err := cloudlink.Read(dir, apiURL)
	if err != nil {
		t.Fatalf("cloudlink.Read: %v", err)
	}
	return link
}

func TestRunLink_NotLoggedIn_ReturnsExitErrorWithLoginInstruction(t *testing.T) {
	prev := loadCredentials
	loadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{}, credentials.ErrNotLoggedIn
	}
	defer func() { loadCredentials = prev }()

	var stderr bytes.Buffer
	err := runLink(context.Background(), t.TempDir(), "", linkOptions{}, &bytes.Buffer{}, &stderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runLink err = %v (%T), want *ExitError", err, err)
	}
	if !strings.Contains(stderr.String(), "ocel login") {
		t.Fatalf("stderr = %q, want it to mention `ocel login`", stderr.String())
	}
}

func TestRunLink_SelectsExistingProjectBySlug(t *testing.T) {
	setLoggedIn(t)
	srv := newCloudServer(t, project("p1", "My App", "my-app"), project("p2", "Other", "other"))
	dir := t.TempDir()

	opts := linkOptions{apiURL: srv.URL}
	if err := runLink(context.Background(), dir, "other", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader("")); err != nil {
		t.Fatalf("runLink err = %v", err)
	}

	link := readLink(t, dir, srv.URL)
	if link == nil {
		t.Fatal("no link written")
	}
	want := cloudlink.Link{APIURL: srv.URL, OrganizationID: "org_1", ProjectID: "p2", ProjectName: "Other"}
	if *link != want {
		t.Fatalf("link = %+v, want %+v", *link, want)
	}
	if len(srv.created) != 0 {
		t.Fatalf("created = %v, want no project creation", srv.created)
	}
	if srv.setActive != 1 {
		t.Fatalf("set-active calls = %d, want 1", srv.setActive)
	}
}

func TestRunLink_UnknownSlug_ErrorsListingAvailableProjects(t *testing.T) {
	setLoggedIn(t)
	srv := newCloudServer(t, project("p1", "My App", "my-app"))

	opts := linkOptions{apiURL: srv.URL}
	err := runLink(context.Background(), t.TempDir(), "nope", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil {
		t.Fatal("runLink err = nil, want error")
	}
	if !strings.Contains(err.Error(), "my-app") {
		t.Fatalf("err = %v, want it to list the available slugs", err)
	}
}

func TestRunLink_NonTTYWithoutProjectOrCreate_ErrorsAboutFlags(t *testing.T) {
	setLoggedIn(t)
	srv := newCloudServer(t, project("p1", "My App", "my-app"))

	dir := t.TempDir()
	opts := linkOptions{apiURL: srv.URL}
	err := runLink(context.Background(), dir, "", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil {
		t.Fatal("runLink err = nil, want error")
	}
	if !strings.Contains(err.Error(), "--create") {
		t.Fatalf("err = %v, want it to mention --create", err)
	}
	if readLink(t, dir, srv.URL) != nil {
		t.Fatal("a link was written despite the error")
	}
}

func TestRunLink_CreateWithoutName_UsesDirectoryName(t *testing.T) {
	setLoggedIn(t)
	srv := newCloudServer(t)

	dir := filepath.Join(t.TempDir(), "my-fresh-app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	opts := linkOptions{apiURL: srv.URL, create: true}
	if err := runLink(context.Background(), dir, "", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader("")); err != nil {
		t.Fatalf("runLink err = %v", err)
	}

	if len(srv.created) != 1 || srv.created[0]["slug"] != "my-fresh-app" {
		t.Fatalf("created = %v, want one project slugged after the directory", srv.created)
	}
	link := readLink(t, dir, srv.URL)
	if link == nil || link.ProjectID != "proj_new" {
		t.Fatalf("link = %+v, want the created project", link)
	}
}

func TestRunLink_CreateWithName_SlugifiesIt(t *testing.T) {
	setLoggedIn(t)
	srv := newCloudServer(t)

	opts := linkOptions{apiURL: srv.URL, create: true}
	if err := runLink(context.Background(), t.TempDir(), "My Cool App", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader("")); err != nil {
		t.Fatalf("runLink err = %v", err)
	}
	if len(srv.created) != 1 || srv.created[0]["slug"] != "my-cool-app" || srv.created[0]["name"] != "My Cool App" {
		t.Fatalf("created = %v, want name/slug from the argument", srv.created)
	}
}

func TestRunLink_CreateConflict_PointsAtLinkingToTheExistingProject(t *testing.T) {
	setLoggedIn(t)
	srv := newCloudServer(t)
	srv.createConflict = true

	opts := linkOptions{apiURL: srv.URL, create: true}
	err := runLink(context.Background(), t.TempDir(), "My App", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil {
		t.Fatal("runLink err = nil, want error")
	}
	if !strings.Contains(err.Error(), "ocel link my-app") {
		t.Fatalf("err = %v, want it to suggest `ocel link my-app`", err)
	}
}

func TestRunLink_MultipleOrgsNonTTY_RequiresOrgFlag(t *testing.T) {
	setLoggedIn(t)
	srv := newCloudServer(t)
	srv.orgs = append(srv.orgs, map[string]string{"id": "org_2", "name": "Other Co", "slug": "other-co"})

	opts := linkOptions{apiURL: srv.URL, create: true}
	err := runLink(context.Background(), t.TempDir(), "My App", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "--org") {
		t.Fatalf("runLink err = %v, want it to mention --org", err)
	}
}

func TestRunLink_OrgFlagSelectsAmongSeveral(t *testing.T) {
	setLoggedIn(t)
	srv := newCloudServer(t)
	srv.orgs = append(srv.orgs, map[string]string{"id": "org_2", "name": "Other Co", "slug": "other-co"})

	dir := t.TempDir()
	opts := linkOptions{apiURL: srv.URL, create: true, org: "other-co"}
	if err := runLink(context.Background(), dir, "My App", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader("")); err != nil {
		t.Fatalf("runLink err = %v", err)
	}
	link := readLink(t, dir, srv.URL)
	if link == nil || link.OrganizationID != "org_2" {
		t.Fatalf("link = %+v, want organizationId org_2", link)
	}
}

func TestRunLink_UnknownOrgFlag_Errors(t *testing.T) {
	setLoggedIn(t)
	srv := newCloudServer(t)

	opts := linkOptions{apiURL: srv.URL, create: true, org: "nope"}
	err := runLink(context.Background(), t.TempDir(), "My App", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "acme-inc") {
		t.Fatalf("runLink err = %v, want it to list the available org slugs", err)
	}
}

func TestRunLink_RelinkingReportsThePreviousLinkAndReplacesIt(t *testing.T) {
	setLoggedIn(t)
	srv := newCloudServer(t, project("p1", "My App", "my-app"), project("p2", "Other", "other"))

	dir := t.TempDir()
	if err := cloudlink.Write(dir, cloudlink.Link{
		APIURL: srv.URL, OrganizationID: "org_1", ProjectID: "p1", ProjectName: "My App",
	}); err != nil {
		t.Fatalf("seed link: %v", err)
	}

	var stdout bytes.Buffer
	opts := linkOptions{apiURL: srv.URL}
	if err := runLink(context.Background(), dir, "other", opts, &stdout, &bytes.Buffer{}, strings.NewReader("")); err != nil {
		t.Fatalf("runLink err = %v", err)
	}
	if !strings.Contains(stdout.String(), "linked to My App") {
		t.Fatalf("stdout = %q, want it to report the previous link", stdout.String())
	}
	if link := readLink(t, dir, srv.URL); link == nil || link.ProjectID != "p2" {
		t.Fatalf("link = %+v, want it replaced with p2", link)
	}
}

// A record naming another control plane reads as unlinked, so `ocel link`
// against a different --api-url must not announce a previous link, and must
// overwrite the record rather than reconcile with it.
func TestRunLink_IgnoresARecordFromAnotherControlPlane(t *testing.T) {
	setLoggedIn(t)
	srv := newCloudServer(t, project("p1", "My App", "my-app"))

	dir := t.TempDir()
	if err := cloudlink.Write(dir, cloudlink.Link{
		APIURL: "https://elsewhere.example.com", OrganizationID: "org_9", ProjectID: "p9", ProjectName: "Elsewhere",
	}); err != nil {
		t.Fatalf("seed link: %v", err)
	}

	var stdout bytes.Buffer
	opts := linkOptions{apiURL: srv.URL}
	if err := runLink(context.Background(), dir, "my-app", opts, &stdout, &bytes.Buffer{}, strings.NewReader("")); err != nil {
		t.Fatalf("runLink err = %v", err)
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
}

func TestRunUnlink_RemovesTheRecord(t *testing.T) {
	dir := t.TempDir()
	if err := cloudlink.Write(dir, cloudlink.Link{APIURL: "https://ocel.app", ProjectID: "p1"}); err != nil {
		t.Fatalf("seed link: %v", err)
	}

	var stdout bytes.Buffer
	if err := runUnlink(dir, &stdout); err != nil {
		t.Fatalf("runUnlink err = %v", err)
	}
	if !strings.Contains(stdout.String(), "Unlinked") {
		t.Fatalf("stdout = %q, want it to confirm the unlink", stdout.String())
	}
	if link := readLink(t, dir, "https://ocel.app"); link != nil {
		t.Fatalf("link = %+v after unlink, want nil", link)
	}
}

func TestRunUnlink_NothingToRemoveIsNotAnError(t *testing.T) {
	var stdout bytes.Buffer
	if err := runUnlink(t.TempDir(), &stdout); err != nil {
		t.Fatalf("runUnlink err = %v, want nil", err)
	}
	if !strings.Contains(stdout.String(), "isn't linked") {
		t.Fatalf("stdout = %q, want it to say the directory isn't linked", stdout.String())
	}
}
