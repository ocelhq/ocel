package images_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/provider"
)

type packageVersion struct {
	id   int
	name string
	tags []string
}

type fakeGitHub struct {
	mu sync.Mutex

	scope    string
	pkg      string
	versions []packageVersion

	throttled   int
	scopeless   bool
	asked       []string
	tokens      []string
	pkgDeleted  bool
	deletedRefs []int
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, r.Method+" "+r.URL.EscapedPath())
	f.tokens = append(f.tokens, r.Header.Get("Authorization"))
	if f.throttled > 0 {
		f.throttled--
		w.Header().Set("Retry-After", "0")
		http.Error(w, `{"message":"You have exceeded a secondary rate limit"}`, http.StatusTooManyRequests)
		return
	}
	root := "/" + f.scope + "/packages/container/" + strings.ReplaceAll(f.pkg, "/", "%2F")
	path := r.URL.EscapedPath()
	rest, ours := strings.CutPrefix(path, root)
	if !ours || f.pkgDeleted {
		http.Error(w, `{"message":"Package not found."}`, http.StatusNotFound)
		return
	}
	switch {
	case r.Method == http.MethodGet && rest == "/versions":
		f.listVersions(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(rest, "/versions/"):
		f.deleteVersion(w, strings.TrimPrefix(rest, "/versions/"))
	case r.Method == http.MethodDelete && rest == "":
		if f.scopeless {
			http.Error(w, `{"message":"You need at least delete:packages scope"}`, http.StatusForbidden)
			return
		}
		f.pkgDeleted = true
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unexpected", http.StatusTeapot)
	}
}

func (f *fakeGitHub) listVersions(w http.ResponseWriter, r *http.Request) {
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage == 0 {
		perPage = 30
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page == 0 {
		page = 1
	}
	start := min((page-1)*perPage, len(f.versions))
	end := min(start+perPage, len(f.versions))
	listed := []map[string]any{}
	for _, version := range f.versions[start:end] {
		listed = append(listed, map[string]any{
			"id":       version.id,
			"name":     version.name,
			"metadata": map[string]any{"package_type": "container", "container": map[string]any{"tags": version.tags}},
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(listed)
}

func (f *fakeGitHub) deleteVersion(w http.ResponseWriter, id string) {
	if f.scopeless {
		http.Error(w, `{"message":"You need at least delete:packages scope"}`, http.StatusForbidden)
		return
	}
	at := slices.IndexFunc(f.versions, func(version packageVersion) bool { return strconv.Itoa(version.id) == id })
	if at < 0 {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	if len(f.versions) == 1 {
		http.Error(w, `{"message":"You cannot delete the last tagged version of a package. You must delete the package instead."}`, http.StatusBadRequest)
		return
	}
	f.deletedRefs = append(f.deletedRefs, f.versions[at].id)
	f.versions = slices.Delete(f.versions, at, at+1)
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeGitHub) deletes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var deletes []string
	for _, asked := range f.asked {
		if strings.HasPrefix(asked, http.MethodDelete) {
			deletes = append(deletes, asked)
		}
	}
	return deletes
}

func gitHubServing(t *testing.T, f *fakeGitHub) provider.ImageStore {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(api.Close)
	target := provider.RegistryTarget{Server: "ghcr.io", Namespace: "acme", Username: "acme-bot", Password: "ghp_t0ken"}
	return images.RegistryStoreAnsweredBy(target, api.URL)
}

func shopWebVersions() []packageVersion {
	return []packageVersion{
		{id: 1, name: "sha256:" + strings.Repeat("1", 64), tags: []string{"sha256-old"}},
		{id: 2, name: "sha256:" + strings.Repeat("2", 64), tags: []string{"sha256-going"}},
		{id: 3, name: "sha256:" + strings.Repeat("3", 64), tags: []string{"sha256-kept"}},
	}
}

const ghcrGoing = "ghcr.io/acme/shop.web:sha256-going"

func TestRemovingAnImageFromGHCRDeletesTheOrganizationPackageVersionItsTagNames(t *testing.T) {
	f := &fakeGitHub{scope: "orgs/acme", pkg: "shop.web", versions: shopWebVersions()}

	if err := gitHubServing(t, f).Remove(context.Background(), ghcrGoing); err != nil {
		t.Fatalf("Remove() = %v", err)
	}

	if want := []string{"DELETE /orgs/acme/packages/container/shop.web/versions/2"}; !slices.Equal(f.deletes(), want) {
		t.Errorf("Remove() deleted %v, want %v: ghcr refuses a manifest delete, and a package version is how GitHub deletes an image", f.deletes(), want)
	}
	if f.tokens[0] != "Bearer ghp_t0ken" {
		t.Errorf("Remove() authorized with %q, want the registry password as the token", f.tokens[0])
	}
}

func TestRemovingAnImageFromGHCRFindsAPackageAUserOwns(t *testing.T) {
	f := &fakeGitHub{scope: "users/acme", pkg: "shop.web", versions: shopWebVersions()}

	if err := gitHubServing(t, f).Remove(context.Background(), ghcrGoing); err != nil {
		t.Fatalf("Remove() = %v", err)
	}

	if want := []string{"DELETE /users/acme/packages/container/shop.web/versions/2"}; !slices.Equal(f.deletes(), want) {
		t.Errorf("Remove() deleted %v, want %v", f.deletes(), want)
	}
}

func TestRemovingAnImageFromGHCREscapesTheSlashesOfANestedPackage(t *testing.T) {
	f := &fakeGitHub{scope: "orgs/acme", pkg: "ocel/shop.web", versions: shopWebVersions()}

	if err := gitHubServing(t, f).Remove(context.Background(), "ghcr.io/acme/ocel/shop.web:sha256-going"); err != nil {
		t.Fatalf("Remove() = %v", err)
	}

	if want := []string{"DELETE /orgs/acme/packages/container/ocel%2Fshop.web/versions/2"}; !slices.Equal(f.deletes(), want) {
		t.Errorf("Remove() deleted %v, want %v", f.deletes(), want)
	}
}

func TestRemovingAnImageFromGHCRPagesThroughTheVersions(t *testing.T) {
	var versions []packageVersion
	for id := 1; id <= 150; id++ {
		versions = append(versions, packageVersion{id: id, name: fmt.Sprintf("sha256:%064d", id), tags: []string{fmt.Sprintf("sha256-%d", id)}})
	}
	f := &fakeGitHub{scope: "orgs/acme", pkg: "shop.web", versions: versions}

	if err := gitHubServing(t, f).Remove(context.Background(), "ghcr.io/acme/shop.web:sha256-140"); err != nil {
		t.Fatalf("Remove() = %v", err)
	}

	if want := []string{"DELETE /orgs/acme/packages/container/shop.web/versions/140"}; !slices.Equal(f.deletes(), want) {
		t.Errorf("Remove() deleted %v, want %v: the version sits on the second page", f.deletes(), want)
	}
}

func TestRemovingTheLastVersionFromGHCRDeletesThePackage(t *testing.T) {
	f := &fakeGitHub{scope: "orgs/acme", pkg: "shop.web", versions: []packageVersion{
		{id: 2, name: "sha256:" + strings.Repeat("2", 64), tags: []string{"sha256-going"}},
	}}

	if err := gitHubServing(t, f).Remove(context.Background(), ghcrGoing); err != nil {
		t.Fatalf("Remove() = %v", err)
	}

	if !f.pkgDeleted {
		t.Errorf("Remove() asked %v and left the package: GitHub refuses to delete a package's last version, and the package itself is what goes", f.deletes())
	}
}

func TestRemovingAnImageFromGHCRWithATokenThatMayNotDeleteNamesTheScopeItLacks(t *testing.T) {
	f := &fakeGitHub{scope: "orgs/acme", pkg: "shop.web", versions: shopWebVersions(), scopeless: true}

	err := gitHubServing(t, f).Remove(context.Background(), ghcrGoing)

	if err == nil || !strings.Contains(err.Error(), "delete:packages") {
		t.Errorf("Remove() = %v, want an error naming the delete:packages scope the token lacks", err)
	}
}

func TestRemovingAnImageGHCRDoesNotHaveIsDone(t *testing.T) {
	f := &fakeGitHub{scope: "orgs/acme", pkg: "blog.web", versions: shopWebVersions()}

	if err := gitHubServing(t, f).Remove(context.Background(), ghcrGoing); err != nil {
		t.Errorf("Remove() = %v, want nothing: a package that is already gone is reclaimed", err)
	}
	if deletes := f.deletes(); len(deletes) != 0 {
		t.Errorf("Remove() deleted %v from a package it never found", deletes)
	}
}

func TestRemovingATagGHCRNoLongerListsIsDone(t *testing.T) {
	f := &fakeGitHub{scope: "orgs/acme", pkg: "shop.web", versions: shopWebVersions()}

	if err := gitHubServing(t, f).Remove(context.Background(), "ghcr.io/acme/shop.web:sha256-gone"); err != nil {
		t.Errorf("Remove() = %v, want nothing", err)
	}
	if deletes := f.deletes(); len(deletes) != 0 {
		t.Errorf("Remove() deleted %v for a tag no version carries", deletes)
	}
}

func TestRemovingAnImageFromGHCRLeavesAVersionAnotherTagStillNames(t *testing.T) {
	versions := shopWebVersions()
	versions[1].tags = []string{"sha256-going", "sha256-kept-too"}
	f := &fakeGitHub{scope: "orgs/acme", pkg: "shop.web", versions: versions}

	if err := gitHubServing(t, f).Remove(context.Background(), ghcrGoing); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if deletes := f.deletes(); len(deletes) != 0 {
		t.Errorf("Remove() deleted %v, a version sha256-kept-too still names", deletes)
	}
}

func TestRemovingAnImageFromGHCRRetriesAThrottledRequest(t *testing.T) {
	f := &fakeGitHub{scope: "orgs/acme", pkg: "shop.web", versions: shopWebVersions(), throttled: 2}

	if err := gitHubServing(t, f).Remove(context.Background(), ghcrGoing); err != nil {
		t.Fatalf("Remove() = %v, want the throttled requests retried", err)
	}
	if want := []string{"DELETE /orgs/acme/packages/container/shop.web/versions/2"}; !slices.Equal(f.deletes(), want) {
		t.Errorf("Remove() deleted %v, want %v", f.deletes(), want)
	}
}
