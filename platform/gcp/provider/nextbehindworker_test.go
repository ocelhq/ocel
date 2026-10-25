package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

type wallet struct {
	p      *Provider
	server *googletest.Run
	seed   string
	spec   provider.StackSpec
}

func behindTheWorkerWithItsSeed(t *testing.T) *wallet {
	t.Helper()
	w := &wallet{server: &googletest.Run{}}
	w.p = providerServing(w.server, t)
	adoptBehindTheWorker(t, w.p)
	var err error
	if w.seed, err = cloudflareorigin.ReadPurgeSeed(context.Background(), w.p.resolved, environment.TierProduction, cloudflareKind); err != nil {
		t.Fatal(err)
	}
	w.spec = behindTheWorker(routedNextSpec())
	w.spec.Ref.Name.Release = naming.NewReleaseToken("d1", "f1")
	return w
}

func TestANextServiceBehindTheWorkerIsToldItsTagDatabaseAndThePurgeSecretItSignsWith(t *testing.T) {
	w := behindTheWorkerWithItsSeed(t)

	if _, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	env := googletest.EnvOf(w.server.Created[0].Template.Containers[0])
	if _, told := env["OCEL_ISR_WRITER_URL"]; told {
		t.Errorf("OCEL_ISR_WRITER_URL = %q, want none: no worker keeps this service's pages", env["OCEL_ISR_WRITER_URL"])
	}
	if want := edge.DerivePurgeSecret(w.seed, "shop/prod/web/r1a2b3c4d"); env["OCEL_PURGE_SECRET"] != want {
		t.Errorf("OCEL_PURGE_SECRET = %q, want the deployment's derived secret", env["OCEL_PURGE_SECRET"])
	}
	for _, name := range []string{"OCEL_TAG_DATABASE", "OCEL_STORAGE_COORDINATE", "OCEL_ISR_TAG_NAMESPACE", "OCEL_EDGE_BUCKET", "OCEL_CACHE_BUCKET"} {
		if env[name] == "" {
			t.Errorf("the service is told no %s, and its pages and tags live in Cloud Storage and Firestore", name)
		}
	}
}

func TestANextServiceBehindTheWorkerIsToldWhereTheWorkerPurges(t *testing.T) {
	w := behindTheWorkerWithItsSeed(t)
	w.spec.App.ISR.PurgeURL = "https://shop.acct.workers.dev/purge"

	if _, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if got := googletest.EnvOf(w.server.Created[0].Template.Containers[0])[edge.PurgeURLVar]; got != "https://shop.acct.workers.dev/purge" {
		t.Errorf("%s = %q, want the URL the worker takes purges at", edge.PurgeURLVar, got)
	}
}

func TestANextServiceBehindTheWorkerWhoseStackNamesNoPurgeURLIsToldNone(t *testing.T) {
	w := behindTheWorkerWithItsSeed(t)

	if _, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if got, told := googletest.EnvOf(w.server.Created[0].Template.Containers[0])[edge.PurgeURLVar]; told {
		t.Errorf("%s = %q, want none: nothing names where to purge", edge.PurgeURLVar, got)
	}
}

func TestANextServiceBehindTheWorkerIsToldNoCDNURLMap(t *testing.T) {
	w := behindTheWorkerWithItsSeed(t)

	if _, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil); err != nil {
		t.Fatal(err)
	}
	if got, told := googletest.EnvOf(w.server.Created[0].Template.Containers[0])["OCEL_CDN_URL_MAP"]; told {
		t.Errorf("OCEL_CDN_URL_MAP = %q, want none behind the worker", got)
	}
}

func TestANextServiceBehindTheLoadBalancerIsToldNoPurgeSecret(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	spec := routedNextSpec()
	spec.Edge = codeRunningFront{kind: "alb", shields: true}

	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatal(err)
	}
	env := googletest.EnvOf(server.Created[0].Template.Containers[0])
	for _, name := range []string{"OCEL_ISR_WRITER_URL", "OCEL_PURGE_SECRET"} {
		if got, told := env[name]; told {
			t.Errorf("%s = %q behind the load balancer, want none", name, got)
		}
	}
	if env["OCEL_TAG_DATABASE"] == "" {
		t.Error("the service behind the load balancer is told no tag database")
	}
}

func TestANextAppBehindTheWorkerIsGrantedItsCachePrefixAndItsProjectsTagRecords(t *testing.T) {
	w := behindTheWorkerWithItsSeed(t)

	if _, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil); err != nil {
		t.Fatal(err)
	}
	if got := cacheBindingsOf(w.server.Identities()); len(got) != 2 {
		t.Errorf("storage bindings = %q, want the two over the app's cache prefix in the edge and cache buckets", got)
	}
	if got := tagRecordBindingsOf(w.server.Identities()); len(got) != 1 {
		t.Errorf("tag record bindings = %q, want the one over its project's database of runs and tag records: the app's tags live in Firestore", got)
	}
}

func TestPrerenderSeedsBehindTheWorkerAllGoToCloudStorage(t *testing.T) {
	w := behindTheWorkerWithItsSeed(t)
	root := plantPrerenders(t, map[string]string{"cache/blog/post.cache.json": "{}", "fetch-cache/abc": "x"})

	if _, err := w.p.ProvisionFunctions(context.Background(), builtAt(w.spec, root), nil); err != nil {
		t.Fatal(err)
	}

	got := uploadNames(w.server.Stored())
	slices.Sort(got)
	want := []string{"fetch-cache/shop/prod/web/r1a2b3c4d/abc", "pages/shop/prod/web/r1a2b3c4d/blog/post.cache.json"}
	if !slices.Equal(got, want) {
		t.Errorf("Cloud Storage received %v, want the page entry and the fetch entry both, since nothing else keeps a page", got)
	}
}

func TestANextServiceBehindTheWorkerOnATierWithNoOriginSecretSeedsNoPrerenders(t *testing.T) {
	w := behindTheWorkerWithItsSeed(t)
	root := plantPrerenders(t, map[string]string{"cache/blog/post.cache.json": "{}"})
	w.server.Secrets.Mu.Lock()
	delete(w.server.Secrets.Secrets, w.p.resolved.OriginSecret(environment.TierProduction, cloudflareKind))
	w.server.Secrets.Mu.Unlock()

	_, err := w.p.ProvisionFunctions(context.Background(), builtAt(w.spec, root), nil)

	if refusalCode(err) != refusal.CodeNotReady || !strings.Contains(err.Error(), "origin secret") {
		t.Errorf("ProvisionFunctions = %v, want the not-ready refusal for a tier with no origin secret", err)
	}
	if got := uploadNames(w.server.Stored()); len(got) != 0 {
		t.Errorf("seeded %v before refusing, want nothing written", got)
	}
}

func TestANextServiceBehindTheWorkerIsHandedTheOriginSecretAndAnswersOnItsTagURL(t *testing.T) {
	w := behindTheWorkerWithItsSeed(t)
	secret := originSecretOf(t, w.p)

	functions, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil)
	if err != nil {
		t.Fatal(err)
	}

	service := w.server.Created[0]
	env := googletest.EnvOf(service.Template.Containers[0])
	if env[edge.OriginFrontedVar] != "1" || env[edge.OriginSecretVar] != secret.Current || service.Ingress != run.IngressEverywhere {
		t.Errorf("fronted %q, secret handed %v, ingress %s: want the Next service fronted, demanding the tier's origin secret and reached directly",
			env[edge.OriginFrontedVar], env[edge.OriginSecretVar] == secret.Current, service.Ingress)
	}
	if tag := w.spec.Ref.Name.Release.String(); !strings.HasPrefix(functions[0].URL, "https://"+tag+"---") {
		t.Errorf("URL = %q, want the revision-tag URL of %s", functions[0].URL, tag)
	}
}

func TestOnlyANextServiceBehindTheWorkerIsToldToAnswerImageRequestsOnItsOriginPath(t *testing.T) {
	behind := behindTheWorkerWithItsSeed(t)
	if _, err := behind.p.ProvisionFunctions(context.Background(), behind.spec, nil); err != nil {
		t.Fatal(err)
	}
	if got := googletest.EnvOf(behind.server.Created[0].Template.Containers[0])["OCEL_IMAGE_ENDPOINT"]; got != "1" {
		t.Errorf("OCEL_IMAGE_ENDPOINT = %q behind the worker, want 1: the worker posts images to the origin", got)
	}

	server := &googletest.Run{}
	spec := routedNextSpec()
	spec.Edge = codeRunningFront{kind: "alb", shields: true}
	if _, err := providerServing(server, t).ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatal(err)
	}
	if got, told := googletest.EnvOf(server.Created[0].Template.Containers[0])["OCEL_IMAGE_ENDPOINT"]; told {
		t.Errorf("OCEL_IMAGE_ENDPOINT = %q behind the load balancer, want none: nothing posts images there, and the path would answer anyone", got)
	}
}

func TestANextContainerBuiltFromItsFunctionBehindTheWorkerIsToldThePurgeSecretItSignsWith(t *testing.T) {
	w := behindTheWorkerWithItsSeed(t)

	if _, err := w.p.ProvisionContainers(context.Background(), builtFromItsFunction(w.spec), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	env := googletest.EnvOf(w.server.Created[0].Template.Containers[0])
	if want := edge.DerivePurgeSecret(w.seed, "shop/prod/web/r1a2b3c4d"); env["OCEL_PURGE_SECRET"] != want {
		t.Errorf("OCEL_PURGE_SECRET = %q, want the deployment's derived secret", env["OCEL_PURGE_SECRET"])
	}
}
