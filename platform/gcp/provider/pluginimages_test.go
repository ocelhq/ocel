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
)

type registryServer struct {
	mu         sync.Mutex
	repository bool
	versions   map[string]bool
	created    []string
	uploads    []string
	bodies     []string
}

func (s *registryServer) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
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
			if !s.repository {
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
			s.created = append(s.created, r.URL.Query().Get("repositoryId")+" "+body.Format)
			s.repository = true
			_, _ = io.WriteString(w, `{"name":"operations/make","done":true}`)
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

	image, err := (pluginImages{p: p}).Ensure(context.Background(), []byte("\x00asm-gate"))
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
	if !strings.HasPrefix(image, prefix) || !strings.Contains(image, "/genericArtifacts/previewgate:") {
		t.Errorf("the image is %q, want the resource-name form %s<repository>/genericArtifacts/previewgate:<version>: the docker.pkg.dev form is refused", image, prefix)
	}
}

func TestAPluginAlreadyInTheRegistryIsNotUploadedAgain(t *testing.T) {
	registry := &registryServer{}
	p := pushing(t, registry.serve(t).URL)
	ensure := pluginImages{p: p}
	first, err := ensure.Ensure(context.Background(), []byte("\x00asm-gate"))
	if err != nil {
		t.Fatal(err)
	}

	again, err := ensure.Ensure(context.Background(), []byte("\x00asm-gate"))
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
	first, err := ensure.Ensure(context.Background(), []byte("\x00asm-gate-one"))
	if err != nil {
		t.Fatal(err)
	}

	second, err := ensure.Ensure(context.Background(), []byte("\x00asm-gate-two"))
	if err != nil {
		t.Fatal(err)
	}

	if first == second || len(registry.uploads) != 2 {
		t.Errorf("images %q and %q after %d uploads, want two versions", first, second, len(registry.uploads))
	}
}
