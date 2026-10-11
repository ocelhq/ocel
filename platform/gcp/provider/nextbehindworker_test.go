package gcp

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

type wallet struct {
	p      *Provider
	server *runServer
	seed   string
	spec   provider.StackSpec
}

func behindTheWorkerWithItsSeed(t *testing.T) *wallet {
	t.Helper()
	w := &wallet{server: &runServer{}}
	w.p = w.server.open(t)
	adoptBehindTheWorker(t, w.p)
	var err error
	if w.seed, err = readPurgeSeed(context.Background(), w.p.resolved, environment.TierProduction, cloudflareKind); err != nil {
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

	env := envOf(w.server.created[0].Template.Containers[0])
	if _, told := env["OCEL_ISR_WRITER_URL"]; told {
		t.Errorf("OCEL_ISR_WRITER_URL = %q, want none: no worker keeps this service's pages", env["OCEL_ISR_WRITER_URL"])
	}
	if want := cloudflare.DerivePurgeSecret(w.seed, w.spec.App.ISR.Prefix); env["OCEL_PURGE_SECRET"] != want {
		t.Errorf("OCEL_PURGE_SECRET = %q, want the deployment's derived secret", env["OCEL_PURGE_SECRET"])
	}
	for _, name := range []string{"OCEL_TAG_DATABASE", "OCEL_ISR_PREFIX", "OCEL_ISR_TAG_NAMESPACE", "OCEL_ISR_BUCKET", "OCEL_ISR_OBJECT_PREFIX"} {
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

	if got := envOf(w.server.created[0].Template.Containers[0])[edge.PurgeURLVar]; got != "https://shop.acct.workers.dev/purge" {
		t.Errorf("%s = %q, want the URL the worker takes purges at", edge.PurgeURLVar, got)
	}
}

func TestANextServiceBehindTheWorkerWhoseStackNamesNoPurgeURLIsToldNone(t *testing.T) {
	w := behindTheWorkerWithItsSeed(t)

	if _, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if got, told := envOf(w.server.created[0].Template.Containers[0])[edge.PurgeURLVar]; told {
		t.Errorf("%s = %q, want none: nothing names where to purge", edge.PurgeURLVar, got)
	}
}

func TestANextServiceBehindTheWorkerIsToldNoCDNURLMap(t *testing.T) {
	w := behindTheWorkerWithItsSeed(t)

	if _, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil); err != nil {
		t.Fatal(err)
	}
	if got, told := envOf(w.server.created[0].Template.Containers[0])["OCEL_CDN_URL_MAP"]; told {
		t.Errorf("OCEL_CDN_URL_MAP = %q, want none behind the worker", got)
	}
}

func TestANextServiceBehindTheLoadBalancerIsToldNoPurgeSecret(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	spec := routedNextSpec()
	spec.Edge = codeRunningFront{kind: "alb", shields: true}

	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatal(err)
	}
	env := envOf(server.created[0].Template.Containers[0])
	for _, name := range []string{"OCEL_ISR_WRITER_URL", "OCEL_PURGE_SECRET"} {
		if got, told := env[name]; told {
			t.Errorf("%s = %q behind the load balancer, want none", name, got)
		}
	}
	if env["OCEL_TAG_DATABASE"] == "" {
		t.Error("the service behind the load balancer is told no tag database")
	}
}

func TestANextAppBehindTheWorkerIsGrantedItsCachePrefixAndItsTagDatabase(t *testing.T) {
	w := behindTheWorkerWithItsSeed(t)

	if _, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil); err != nil {
		t.Fatal(err)
	}
	if got := cacheBindingsOf(w.server.identities()); len(got) != 1 {
		t.Errorf("storage bindings = %q, want the one over the app's cache prefix", got)
	}
	if got := tagRecordBindingsOf(w.server.identities()); len(got) != 1 {
		t.Errorf("tag record bindings = %q, want the one over the tier's tag database: the app's tags live in Firestore", got)
	}
}

func TestPrerenderSeedsBehindTheWorkerAllGoToCloudStorage(t *testing.T) {
	w := behindTheWorkerWithItsSeed(t)
	plantPrerenders(t, w.p, map[string]string{"cache/blog/post.cache.json": "{}", "fetch-cache/abc": "x"})

	if _, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil); err != nil {
		t.Fatal(err)
	}

	got := uploadNames(w.server.stored())
	slices.Sort(got)
	want := []string{"cache/shop/web/prod/r1/isr/cache/blog/post.cache.json", "cache/shop/web/prod/r1/isr/fetch-cache/abc"}
	if !slices.Equal(got, want) {
		t.Errorf("Cloud Storage received %v, want the page entry and the fetch entry both, since nothing else keeps a page", got)
	}
}

func TestANextServiceBehindTheWorkerOnATierWithNoOriginWildcardSeedsNoPrerenders(t *testing.T) {
	w := behindTheWorkerWithItsSeed(t)
	plantPrerenders(t, w.p, map[string]string{"cache/blog/post.cache.json": "{}"})
	if err := keyvalue.Forget(context.Background(), w.p.records, originWildcardKey(environment.TierProduction)); err != nil {
		t.Fatal(err)
	}

	_, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil)

	if refusalCode(err) != refusal.CodeInvalid {
		t.Errorf("ProvisionFunctions = %v, want the refusal for a tier with no origin domain", err)
	}
	if got := uploadNames(w.server.stored()); len(got) != 0 {
		t.Errorf("seeded %v before refusing, want nothing written", got)
	}
}

func TestOnlyANextServiceBehindTheWorkerIsToldToAnswerImageRequestsOnItsOriginPath(t *testing.T) {
	behind := behindTheWorkerWithItsSeed(t)
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
