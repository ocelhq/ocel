package gcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

type writerRequests struct {
	mu      sync.Mutex
	entries []string
	auths   []string
}

func (w *writerRequests) puts() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.entries)
}

type wallet struct {
	p      *Provider
	server *runServer
	writer *writerRequests
	url    string
	seed   string
	spec   provider.StackSpec
}

func behindTheWorkerWithItsWriter(t *testing.T) *wallet {
	t.Helper()
	w := &wallet{server: &runServer{}, writer: &writerRequests{}}
	isrWriter := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.writer.mu.Lock()
		defer w.writer.mu.Unlock()
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/entry") {
			w.writer.entries = append(w.writer.entries, r.URL.Path+"?key="+r.URL.Query().Get("key"))
			w.writer.auths = append(w.writer.auths, r.Header.Get("Authorization"))
		}
		rw.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(isrWriter.Close)
	w.url = isrWriter.URL
	w.p = w.server.open(t)
	adoptBehindTheWorker(t, w.p, isrWriter.URL)
	var err error
	if w.seed, err = readISRWriterSeed(context.Background(), w.p.resolved, environment.TierProduction, cloudflareKind); err != nil {
		t.Fatal(err)
	}
	w.spec = behindTheWorker(routedNextSpec())
	w.spec.Ref.Name.Release = naming.NewRelease("d1", "f1")
	return w
}

func TestANextServiceBehindTheWorkerIsToldItsISRWriterAndNoTagDatabase(t *testing.T) {
	w := behindTheWorkerWithItsWriter(t)

	if _, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	env := envOf(w.server.created[0].Template.Containers[0])
	if env["OCEL_ISR_WRITER_URL"] != w.url {
		t.Errorf("OCEL_ISR_WRITER_URL = %q, want the adopted endpoint %q", env["OCEL_ISR_WRITER_URL"], w.url)
	}
	if want := cloudflare.DeriveISRWriteSecret(w.seed, w.spec.App.ISR.Prefix); env["OCEL_ISR_WRITER_SECRET"] != want {
		t.Errorf("OCEL_ISR_WRITER_SECRET = %q, want the deployment's derived secret", env["OCEL_ISR_WRITER_SECRET"])
	}
	for _, name := range []string{"OCEL_TAG_DATABASE", "OCEL_FIRESTORE_ENDPOINT"} {
		if got, told := env[name]; told {
			t.Errorf("the service reads %s=%q, want none: its tags live in the edge's store", name, got)
		}
	}
	for _, name := range []string{"OCEL_ISR_PREFIX", "OCEL_ISR_TAG_NAMESPACE", "OCEL_ISR_BUCKET", "OCEL_ISR_OBJECT_PREFIX"} {
		if env[name] == "" {
			t.Errorf("the service is told no %s, and fetch entries still live in Cloud Storage", name)
		}
	}
}

func TestANextServiceBehindTheWorkerIsToldNoCDNURLMap(t *testing.T) {
	w := behindTheWorkerWithItsWriter(t)

	if _, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil); err != nil {
		t.Fatal(err)
	}
	if got, told := envOf(w.server.created[0].Template.Containers[0])["OCEL_CDN_URL_MAP"]; told {
		t.Errorf("OCEL_CDN_URL_MAP = %q, want none behind the worker", got)
	}
}

func TestANextServiceBehindTheLoadBalancerIsToldNoISRWriter(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	spec := routedNextSpec()
	spec.Edge = codeRunningFront{kind: "alb", shields: true}

	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatal(err)
	}
	env := envOf(server.created[0].Template.Containers[0])
	for _, name := range []string{"OCEL_ISR_WRITER_URL", "OCEL_ISR_WRITER_SECRET"} {
		if got, told := env[name]; told {
			t.Errorf("%s = %q behind the load balancer, want none", name, got)
		}
	}
	if env["OCEL_TAG_DATABASE"] == "" {
		t.Error("the service behind the load balancer is told no tag database")
	}
}

func TestANextAppBehindTheWorkerIsGrantedItsCachePrefixAndNoTagDatabase(t *testing.T) {
	w := behindTheWorkerWithItsWriter(t)

	if _, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil); err != nil {
		t.Fatal(err)
	}
	if got := cacheBindingsOf(w.server.identities()); len(got) != 1 {
		t.Errorf("storage bindings = %q, want the one over the app's cache prefix", got)
	}
	if got := tagRecordBindingsOf(w.server.identities()); len(got) != 0 {
		t.Errorf("tag record bindings = %q, want none: the app's tags are not in Firestore", got)
	}
}

func TestPrerenderSeedsBehindTheWorkerGoThroughTheISRWriter(t *testing.T) {
	w := behindTheWorkerWithItsWriter(t)
	plantPrerenders(t, w.p, map[string]string{"cache/blog/post.cache.json": "{}"})

	if _, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil); err != nil {
		t.Fatal(err)
	}

	prefix := w.spec.App.ISR.Prefix
	if got, want := w.writer.puts(), []string{"/" + prefix + "/entry?key=blog/post"}; !slices.Equal(got, want) {
		t.Errorf("writer entries = %v, want %v", got, want)
	}
	if want := "Bearer " + cloudflare.DeriveISRWriteSecret(w.seed, prefix); w.writer.auths[0] != want {
		t.Errorf("Authorization = %q, want the deployment's write secret", w.writer.auths[0])
	}
	if got := uploadNames(w.server.stored()); len(got) != 0 {
		t.Errorf("Cloud Storage received %v, want the page entry only in the writer", got)
	}
}

func TestFetchSeedsBehindTheWorkerStayInCloudStorage(t *testing.T) {
	w := behindTheWorkerWithItsWriter(t)
	plantPrerenders(t, w.p, map[string]string{"cache/index.cache.json": "{}", "fetch-cache/abc": "x"})

	if _, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil); err != nil {
		t.Fatal(err)
	}

	if got, want := uploadNames(w.server.stored()), []string{"cache/shop/web/prod/r1/isr/fetch-cache/abc"}; !slices.Equal(got, want) {
		t.Errorf("Cloud Storage received %v, want only the fetch entry", got)
	}
	if got := w.writer.puts(); len(got) != 1 {
		t.Errorf("writer entries = %v, want the page entry alone", got)
	}
}

func TestASeedThatIsNoCacheEntryTheWriterCanAddressIsRefused(t *testing.T) {
	w := behindTheWorkerWithItsWriter(t)
	plantPrerenders(t, w.p, map[string]string{"cache/index.json": "{}"})

	_, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil)
	if err == nil || !strings.Contains(err.Error(), "not a cache entry the isr-writer can address") {
		t.Errorf("ProvisionFunctions() = %v, want a refusal naming the seed", err)
	}
}

func TestANextServiceBehindTheWorkerOnATierWithNoOriginWildcardSeedsNoPrerenders(t *testing.T) {
	w := behindTheWorkerWithItsWriter(t)
	plantPrerenders(t, w.p, map[string]string{"cache/blog/post.cache.json": "{}"})
	if err := keyvalue.Forget(context.Background(), w.p.records, originWildcardKey(environment.TierProduction)); err != nil {
		t.Fatal(err)
	}

	_, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil)

	if refusalCode(err) != refusal.CodeInvalid {
		t.Errorf("ProvisionFunctions = %v, want the refusal for a tier with no origin domain", err)
	}
	if puts := w.writer.puts(); len(puts) != 0 {
		t.Errorf("seeded %v before refusing, want nothing written to the edge's store", puts)
	}
}

func TestOnlyANextServiceBehindTheWorkerIsToldToAnswerImageRequestsOnItsOriginPath(t *testing.T) {
	behind := behindTheWorkerWithItsWriter(t)
	if _, err := behind.p.ProvisionFunctions(context.Background(), behind.spec, nil); err != nil {
		t.Fatal(err)
	}
	if got := envOf(behind.server.created[0].Template.Containers[0])["OCEL_IMAGE_ENDPOINT"]; got != "1" {
		t.Errorf("OCEL_IMAGE_ENDPOINT = %q behind the worker, want 1: the worker posts images to the origin", got)
	}

	server := &runServer{}
	spec := routedNextSpec()
	spec.Edge = codeRunningFront{kind: "alb", shields: true}
	if _, err := server.open(t).ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatal(err)
	}
	if got, told := envOf(server.created[0].Template.Containers[0])["OCEL_IMAGE_ENDPOINT"]; told {
		t.Errorf("OCEL_IMAGE_ENDPOINT = %q behind the load balancer, want none: nothing posts images there, and the path would answer anyone", got)
	}
}
