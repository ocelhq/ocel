package gcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

type registryServer struct {
	mu           sync.Mutex
	repositories map[string]bool
	versions     map[string]bool
	created      []string
	uploads      []string
	bodies       []string
	deleted      []string
}

func repositoryOf(rest string) string {
	id, _, _ := strings.Cut(rest, "/")
	return id
}

func (s *registryServer) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.repositories == nil {
			s.repositories = map[string]bool{}
		}
		w.Header().Set("Content-Type", "application/json")
		const repository = "/v1/projects/acme-prod/locations/europe-west1/repositories"
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(path, repository+"/") && strings.Contains(path, "/versions/"):
			version := path[strings.LastIndex(path, "/")+1:]
			if !s.versions[version] {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"error":{"code":404,"status":"NOT_FOUND","message":"no such version"}}`)
				return
			}
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodGet && strings.HasPrefix(path, repository+"/"):
			if !s.repositories[repositoryOf(path[len(repository)+1:])] {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"error":{"code":404,"status":"NOT_FOUND","message":"no such repository"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"format":"GENERIC"}`)
		case r.Method == http.MethodPost && path == repository:
			var body struct {
				Format string `json:"format"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			id := r.URL.Query().Get("repositoryId")
			s.created = append(s.created, id+" "+body.Format)
			s.repositories[id] = true
			_, _ = io.WriteString(w, `{"name":"operations/make","done":true}`)
		case r.Method == http.MethodDelete && strings.HasPrefix(path, repository+"/"):
			id := repositoryOf(path[len(repository)+1:])
			if !s.repositories[id] {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"error":{"code":404,"status":"NOT_FOUND","message":"no such repository"}}`)
				return
			}
			delete(s.repositories, id)
			s.deleted = append(s.deleted, id)
			_, _ = io.WriteString(w, `{"name":"operations/remove","done":true}`)
		case r.Method == http.MethodPost && strings.Contains(path, "/genericArtifacts:create"):
			body, _ := io.ReadAll(r.Body)
			s.uploads = append(s.uploads, path)
			s.bodies = append(s.bodies, string(body))
			if s.versions == nil {
				s.versions = map[string]bool{}
			}
			for _, field := range strings.Split(string(body), `"versionId":"`)[1:] {
				version, _, _ := strings.Cut(field, `"`)
				s.versions[version] = true
			}
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("the registry was asked for %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotImplemented)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestThePluginImageIsUploadedToAGenericRepositoryTheBootstrapCreates(t *testing.T) {
	registry := &registryServer{}
	p := pushing(t, registry.serve(t).URL)

	image, err := (pluginImages{p: p}).Ensure(context.Background(), environment.TierPreview, []byte("\x00asm-gate"))
	if err != nil {
		t.Fatalf("Ensure = %v", err)
	}

	if len(registry.created) != 1 || !strings.HasSuffix(registry.created[0], " GENERIC") {
		t.Errorf("the repository was created as %v, want one generic repository", registry.created)
	}
	if len(registry.uploads) != 1 {
		t.Fatalf("the plugin was uploaded %d times, want once", len(registry.uploads))
	}
	const prefix = "projects/acme-prod/locations/europe-west1/repositories/"
	if !strings.HasPrefix(image, prefix) || !strings.Contains(image, "/genericArtifacts/gate:") {
		t.Errorf("the image is %q, want the resource-name form %s<repository>/genericArtifacts/gate:<version>: the docker.pkg.dev form is refused", image, prefix)
	}
}

func TestEachTiersPluginLivesInARepositoryOfItsOwn(t *testing.T) {
	registry := &registryServer{}
	p := pushing(t, registry.serve(t).URL)
	images := pluginImages{p: p}
	preview, err := images.Ensure(context.Background(), environment.TierPreview, []byte("\x00asm-gate"))
	if err != nil {
		t.Fatal(err)
	}
	production, err := images.Ensure(context.Background(), environment.TierProduction, []byte("\x00asm-gate"))
	if err != nil {
		t.Fatal(err)
	}

	if len(registry.created) != 2 || preview == production {
		t.Fatalf("created %v with images %q and %q, want a repository and an image per tier", registry.created, preview, production)
	}
	if err := images.Delete(context.Background(), environment.TierProduction); err != nil {
		t.Fatal(err)
	}
	if len(registry.deleted) != 1 || !strings.Contains(registry.deleted[0], string(environment.TierProduction)) {
		t.Errorf("deleting production's plugin deleted %v, want the production repository alone: the preview tier's plugin still runs from its own", registry.deleted)
	}
}

func TestAPluginAlreadyInTheRegistryIsNotUploadedAgain(t *testing.T) {
	registry := &registryServer{}
	p := pushing(t, registry.serve(t).URL)
	ensure := pluginImages{p: p}
	first, err := ensure.Ensure(context.Background(), environment.TierPreview, []byte("\x00asm-gate"))
	if err != nil {
		t.Fatal(err)
	}

	again, err := ensure.Ensure(context.Background(), environment.TierPreview, []byte("\x00asm-gate"))
	if err != nil {
		t.Fatal(err)
	}

	if again != first {
		t.Errorf("the same plugin names two images, %q and %q", first, again)
	}
	if len(registry.uploads) != 1 || len(registry.created) != 1 {
		t.Errorf("uploads %d, repositories created %d, want one of each: the image is named by its bytes", len(registry.uploads), len(registry.created))
	}
}

func TestANewPluginBuildIsUploadedAsANewVersion(t *testing.T) {
	registry := &registryServer{}
	p := pushing(t, registry.serve(t).URL)
	ensure := pluginImages{p: p}
	first, err := ensure.Ensure(context.Background(), environment.TierPreview, []byte("\x00asm-gate-one"))
	if err != nil {
		t.Fatal(err)
	}

	second, err := ensure.Ensure(context.Background(), environment.TierPreview, []byte("\x00asm-gate-two"))
	if err != nil {
		t.Fatal(err)
	}

	if first == second || len(registry.uploads) != 2 {
		t.Errorf("images %q and %q after %d uploads, want two versions", first, second, len(registry.uploads))
	}
}

func TestDeletingThePluginImagesRemovesTheRepositoryTheyWereUploadedTo(t *testing.T) {
	registry := &registryServer{}
	p := pushing(t, registry.serve(t).URL)
	images := pluginImages{p: p}
	if _, err := images.Ensure(context.Background(), environment.TierPreview, []byte("\x00asm-gate")); err != nil {
		t.Fatal(err)
	}

	if err := images.Delete(context.Background(), environment.TierPreview); err != nil {
		t.Fatalf("Delete = %v", err)
	}

	if len(registry.deleted) != 1 || !strings.HasSuffix(registry.deleted[0], "-gate") {
		t.Errorf("deleted %v, want the plugin's own repository", registry.deleted)
	}
	if err := images.Delete(context.Background(), environment.TierPreview); err != nil {
		t.Errorf("Delete of a repository already gone = %v, want nil: a teardown that is run again finishes", err)
	}
}
