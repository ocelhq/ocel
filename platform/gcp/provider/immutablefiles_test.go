package gcp

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	raw "google.golang.org/api/storage/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/assets"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

const tierBucket = "ocel-prod-edge-k3x9q2"

type objectsServer struct {
	mu      sync.Mutex
	objects map[string]*raw.Object
	copies  int
}

func servingObjects(t *testing.T, objects ...*raw.Object) (*Provider, *objectsServer) {
	t.Helper()
	server := &objectsServer{objects: map[string]*raw.Object{}}
	for _, object := range objects {
		server.objects[object.Name] = object
	}
	served := httptest.NewServer(server.serve(t))
	t.Cleanup(served.Close)
	return pushing(t, served.URL), server
}

func (s *objectsServer) serve(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := strings.TrimPrefix(r.URL.EscapedPath(), "/storage/v1/")
		parts := strings.Split(path, "/")
		switch {
		case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "b" && parts[1] == tierBucket && parts[2] == "o":
			s.list(w, r.URL.Query().Get("prefix"))
		case r.Method == http.MethodPost && strings.Contains(path, "/rewriteTo/"):
			source, target, _ := strings.Cut(path, "/rewriteTo/")
			src, _ := url.PathUnescape(strings.TrimPrefix(source, "b/"+tierBucket+"/o/"))
			dst, _ := url.PathUnescape(strings.TrimPrefix(target, "b/"+tierBucket+"/o/"))
			s.rewrite(w, r, src, dst)
		default:
			t.Errorf("the copy called %s %s, which nothing here serves", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (s *objectsServer) list(w http.ResponseWriter, prefix string) {
	listed := &raw.Objects{Kind: "storage#objects"}
	for _, name := range slices.Sorted(maps.Keys(s.objects)) {
		if strings.HasPrefix(name, prefix) {
			listed.Items = append(listed.Items, s.objects[name])
		}
	}
	googletest.WriteBody(w, listed)
}

func (s *objectsServer) rewrite(w http.ResponseWriter, r *http.Request, src, dst string) {
	if _, held := s.objects[dst]; held && r.URL.Query().Get("ifGenerationMatch") == "0" {
		w.WriteHeader(http.StatusPreconditionFailed)
		w.Write([]byte(`{"error":{"code":412,"message":"At least one of the pre-conditions you specified did not hold."}}`))
		return
	}
	body, _ := io.ReadAll(r.Body)
	var written raw.Object
	_ = json.Unmarshal(body, &written)
	written.Name, written.Bucket = dst, tierBucket
	s.objects[dst] = &written
	s.copies++
	googletest.WriteBody(w, &raw.RewriteResponse{Kind: "storage#rewriteResponse", Done: true, Resource: &written})
}

func (s *objectsServer) object(name string) *raw.Object {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objects[name]
}

func (s *objectsServer) copied() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.copies
}

func staticObject(name, cacheControl string, metadata map[string]string) *raw.Object {
	return &raw.Object{Name: name, Bucket: tierBucket, ContentType: "text/javascript", CacheControl: cacheControl, Metadata: metadata}
}

func releaseRoutes(release string) alb.StaticRoutes {
	return alb.StaticRoutes{
		Backend:    "ocel-alb-production-assets",
		Objects:    "/assets/production/shop/web/" + release + "/assets",
		Immutable:  []string{"/_next/static/"},
		Revalidate: []string{"/_next/static/live/"},
	}
}

const immutableCache = "public, max-age=31536000, immutable"

func TestCopyingImmutableFilesGivesTheTargetReleaseTheFilesOnlyTheSourceUploaded(t *testing.T) {
	t.Parallel()

	p, server := servingObjects(t,
		staticObject("assets/production/shop/web/r1/assets/_next/static/chunks/a1.js", immutableCache, nil),
		staticObject("assets/production/shop/web/r2/assets/_next/static/chunks/b2.js", immutableCache, nil),
	)
	if err := p.objects().CopyImmutableFiles(context.Background(), environment.TierProduction, releaseRoutes("r1"), releaseRoutes("r2")); err != nil {
		t.Fatalf("CopyImmutableFiles = %v", err)
	}

	copied := server.object("assets/production/shop/web/r2/assets/_next/static/chunks/a1.js")
	if copied == nil {
		t.Fatal("r2 holds no a1.js, want r1's chunk copied under r2's prefix: a page r1 rendered may be answered while the url map already rewrites onto r2")
	}
	if copied.ContentType != "text/javascript" || copied.CacheControl != immutableCache {
		t.Errorf("the copy is typed %q and cached %q, want the source's %q and %q", copied.ContentType, copied.CacheControl, "text/javascript", immutableCache)
	}
	if copied.Metadata[assets.CopiedFromKey] == "" {
		t.Errorf("the copy carries metadata %v, want it marked as a copy so the next promotion does not pass it on", copied.Metadata)
	}
}

func TestCopyingImmutableFilesSkipsWhatTheTargetHoldsWhatTheSourceCopiedAndWhatMustRevalidate(t *testing.T) {
	t.Parallel()

	p, server := servingObjects(t,
		staticObject("assets/production/shop/web/r1/assets/_next/static/chunks/shared.js", immutableCache, nil),
		staticObject("assets/production/shop/web/r2/assets/_next/static/chunks/shared.js", immutableCache, nil),
		staticObject("assets/production/shop/web/r1/assets/_next/static/chunks/old.js", immutableCache, map[string]string{assets.CopiedFromKey: "/assets/production/shop/web/r0/assets"}),
		staticObject("assets/production/shop/web/r1/assets/_next/static/live/feed.json", "no-cache", nil),
	)
	if err := p.objects().CopyImmutableFiles(context.Background(), environment.TierProduction, releaseRoutes("r1"), releaseRoutes("r2")); err != nil {
		t.Fatalf("CopyImmutableFiles = %v", err)
	}
	if got := server.copied(); got != 0 {
		t.Errorf("the copy wrote %d objects, want none: r2 holds shared.js, old.js came to r1 from another release, and live/ is answered by Cloud Run", got)
	}
}
