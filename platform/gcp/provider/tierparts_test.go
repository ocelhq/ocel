package gcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albpart"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
)

func shippingApp(name, framework string) provider.AppEntry {
	return provider.AppEntry{App: name, Manifest: &contractv1.ManifestApp{
		Name:      name,
		Framework: &contractv1.Framework{Name: framework},
		Artifact:  &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{}},
	}}
}

func deployingApps(tier environment.Tier, apps ...provider.AppEntry) provider.DeployPreflight {
	return provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: tier, Slug: "shop", Apps: apps}}
}

func recordedCurrent(t *testing.T, p *Provider, tier environment.Tier, parts ...bootstrap.ResourcePart) {
	t.Helper()
	ctx := context.Background()
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range parts {
		current := stackrecords.BootstrapPart{WrittenBy: string(b.WrittenBy), Digest: part.For(b, tier).Digest(), AddedBy: "shop"}
		if err := stackrecords.MarkBootstrapPartReady(ctx, p.KeyValues(), tier, part.Name, current); err != nil {
			t.Fatal(err)
		}
	}
}

func servingOnlyEnabledAPIs(t *testing.T) string {
	t.Helper()
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.Contains(r.URL.Path, "/projects/acme-prod/services/") {
			t.Errorf("a plain deploy's preflight called %s %s, want only whether the APIs it runs on are on", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		googletest.WriteBody(w, map[string]any{"name": r.URL.Path, "state": bootstrap.EnabledService})
	}))
	t.Cleanup(served.Close)
	return served.URL
}

func TestEachPartADeployAddsHoldsWhatItsFirstUseNeeds(t *testing.T) {
	t.Parallel()

	named := names.New("ocel", "acme-prod")
	tier := environment.TierProduction
	for part, want := range map[string][]string{
		bootstrap.EdgeBucketPart:  {string(bootstrap.KindBucket) + "/" + named.TierBucketStem(tier, naming.StorageEdge)},
		bootstrap.CacheBucketPart: {string(bootstrap.KindBucket) + "/" + named.TierBucketStem(tier, naming.StorageCache)},
		bootstrap.KeysPart:        {string(bootstrap.KindKeyRing) + "/" + named.KeyRing(), string(bootstrap.KindKey) + "/" + string(tier)},
		bootstrap.RealtimePart:    {string(bootstrap.KindServiceAccount) + "/" + named.RealtimeAccount(tier)},
		bootstrap.TopicsPart:      {string(bootstrap.KindServiceAccount) + "/" + named.PushAccount(tier), string(bootstrap.KindQueue) + "/" + named.DelayQueue(tier)},
	} {
		at := slices.IndexFunc(bootstrap.DeployedParts(), func(each bootstrap.ResourcePart) bool { return each.Name == part })
		if at < 0 {
			t.Errorf("no part a deploy adds is named %s", part)
			continue
		}
		if got := googletest.IdsOf(bootstrap.DeployedParts()[at].Items(named, tier)); !slices.Equal(got, want) {
			t.Errorf("the %s part holds %v, want %v", part, got, want)
		}
	}
}

func TestAPlainDeployOfOneHTTPAppAddsNoPartAndPlansNothing(t *testing.T) {
	p := pushing(t, servingOnlyEnabledAPIs(t))
	pre := deployingApps(environment.TierProduction, shippingApp("api", buildoutput.FrameworkGo))

	parts, err := p.partsUsedBy(pre)
	if err != nil || len(parts) != 0 {
		t.Errorf("partsUsedBy() of one HTTP app = %v, %v, want no part: it needs no realtime, topics, tasks, env source, key, cache or edge bucket", parts, err)
	}
	plan, err := p.PlanPreflight(context.Background(), pre)
	if err != nil || len(plan.Groups) != 0 {
		t.Errorf("PlanPreflight() of one HTTP app = %+v, %v, want nothing planned", plan.Groups, err)
	}
	if err := p.ApplyPreflight(context.Background(), pre); err != nil {
		t.Errorf("ApplyPreflight() of one HTTP app = %v", err)
	}
}

func TestADeployAddsThePartsWhatItDeclaresAndShipsNeedOnFirstUse(t *testing.T) {
	t.Parallel()

	secret := shippingApp("api", buildoutput.FrameworkGo)
	secret.Manifest.Variables = []*contractv1.ManifestVariable{{Key: "STRIPE_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET}}
	for name, tc := range map[string]struct {
		pre  provider.DeployPreflight
		want []string
	}{
		"a realtime channel": {
			pre:  provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: environment.TierProduction}, Resources: []provider.Resource{{Name: "chat", Type: provider.BindingRealtime}}},
			want: []string{bootstrap.RealtimePart, bootstrap.KeysPart},
		},
		"a topic": {
			pre:  provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: environment.TierProduction}, Resources: []provider.Resource{{Name: "orders", Type: provider.BindingTopic, Topic: &provider.TopicSpec{}}}},
			want: []string{bootstrap.TopicsPart, bootstrap.KeysPart},
		},
		"a task": {
			pre:  declaringATask(),
			want: []string{bootstrap.TopicsPart, bootstrap.KeysPart},
		},
		"a database": {
			pre:  declaringADatabase("17"),
			want: []string{network.PartName, bootstrap.KeysPart},
		},
		"a kv store": {
			pre:  declaringAStore(1 << 30),
			want: []string{network.PartName, bootstrap.KeysPart},
		},
		"a bucket": {
			pre:  provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: environment.TierProduction}, Resources: []provider.Resource{{Name: "uploads", Type: provider.BindingBucket}}},
			want: []string{bootstrap.KeysPart},
		},
		"a secret value": {
			pre:  deployingApps(environment.TierProduction, secret),
			want: []string{bootstrap.KeysPart},
		},
		"a preview": {
			pre:  deployingApps(environment.TierPreview, shippingApp("api", buildoutput.FrameworkGo)),
			want: []string{bootstrap.KeysPart},
		},
		"a Next app": {
			pre:  deployingApps(environment.TierProduction, shippingApp("web", buildoutput.FrameworkNext)),
			want: []string{bootstrap.EdgeBucketPart, bootstrap.CacheBucketPart},
		},
		"a build that revalidates": {
			pre: provider.DeployPreflight{
				Deploy: provider.DeploySpec{Tier: environment.TierProduction, Apps: []provider.AppEntry{shippingApp("api", buildoutput.FrameworkNode)}},
				Uses:   []buildoutput.Uses{{Revalidation: true}},
			},
			want: []string{bootstrap.CacheBucketPart},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := pushing(t, "").partsUsedBy(tc.pre)
			slices.Sort(got)
			slices.Sort(tc.want)
			if err != nil || !slices.Equal(got, tc.want) {
				t.Errorf("partsUsedBy() of %s = %v, %v, want %v", name, got, err, tc.want)
			}
		})
	}
}

func TestADeployBehindAnEdgeThatServesFromTheTierAddsTheEdgeBucketAndTheKeys(t *testing.T) {
	t.Parallel()

	for _, front := range []edge.Kind{alb.Kind, cloudflare.Kind} {
		pre := deployingApps(environment.TierProduction, shippingApp("api", buildoutput.FrameworkGo))
		pre.Edge = front
		got, err := pushing(t, "").partsUsedBy(pre)
		if err != nil || !slices.Contains(got, bootstrap.EdgeBucketPart) || !slices.Contains(got, bootstrap.KeysPart) {
			t.Errorf("partsUsedBy() behind %s = %v, %v, want the edge bucket the edge serves from and the keys its records are sealed under", front, got, err)
		}
	}
	pre := deployingApps(environment.TierProduction, shippingApp("api", buildoutput.FrameworkGo))
	pre.Edge = alb.Kind
	got, _ := pushing(t, "").partsUsedBy(pre)
	if edgeAt, albAt := slices.Index(got, bootstrap.EdgeBucketPart), slices.Index(got, albpart.PartName); albAt < 0 || edgeAt > albAt {
		t.Errorf("partsUsedBy() behind the alb = %v, want the edge bucket named before the load balancer that serves from it", got)
	}
}

func TestADryDeployOfARealtimeChannelPlansTheRealtimePartAndMakesNothing(t *testing.T) {
	p := failingOnAnyCall(t)
	pre := provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: environment.TierProduction}, Resources: []provider.Resource{{Name: "chat", Type: provider.BindingRealtime}}}

	plan, err := p.PlanPreflight(context.Background(), pre)
	if err != nil {
		t.Fatalf("PlanPreflight() = %v", err)
	}
	account := namesOf(t, p).RealtimeAccount(environment.TierProduction)
	at := slices.IndexFunc(plan.Groups, func(group provider.ChangeGroup) bool { return group.Part == bootstrap.RealtimePart })
	if at < 0 || plan.Groups[at].Action != provider.ActionCreate ||
		len(plan.Groups[at].Changes) != 1 || plan.Groups[at].Changes[0].Name != account || !strings.HasPrefix(plan.Groups[at].Reason, "added on first use") {
		t.Errorf("a dry deploy of a realtime channel planned %+v, want the %s part added on first use with the %s account", plan.Groups, bootstrap.RealtimePart, account)
	}
}

func TestADeployDeclaringAResourcePlansTheKeysItsPublishedBindingIsSealedUnder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tier := environment.TierProduction
	pre := provider.DeployPreflight{
		Deploy:    provider.DeploySpec{Tier: tier, Slug: "shop", Apps: []provider.AppEntry{shippingApp("api", buildoutput.FrameworkGo)}},
		Resources: []provider.Resource{{Name: "uploads", Type: provider.BindingBucket}},
	}
	p, server := sealingWithNoKeys(t)
	parts, err := p.partsUsedBy(pre)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Cipher().Seal(ctx, tier, seal.BindToProject("shop", "binding", string(tier), "production", "uploads"), []byte(`{"bucket":"b"}`)); err != nil {
		t.Fatalf("Seal() of the bucket's published binding = %v", err)
	}
	if !server.HasKeyRing(gcloud.KeyRingPath(p.resolved)) || !server.HasKey(gcloud.KeyPath(p.resolved, string(tier))) {
		t.Fatal("publishing the bucket's binding made no key ring or tier key, so this test proves nothing")
	}
	if !slices.Contains(parts, bootstrap.KeysPart) {
		t.Errorf("partsUsedBy() of a deploy declaring a bucket = %v, want the keys part its published binding's seal adds, so the plan, the record and the price name it", parts)
	}
}

func usedPart(t *testing.T, b bootstrap.Engine, tier environment.Tier, part string, users ...string) {
	t.Helper()
	ctx := context.Background()
	if err := stackrecords.MarkBootstrapPartReady(ctx, b.Records, tier, part, stackrecords.BootstrapPart{AddedBy: "shop"}); err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		if _, err := b.Records.Write(ctx, records.Entry{Key: stackrecords.ProjectKey(tier, user), Value: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
		if err := stackrecords.RecordPartsUsed(ctx, b.Records, tier, user, []string{part}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAPartADeployAddedIsKeptWhileAProjectUsesItAndRetiresWithItsLastUser(t *testing.T) {
	ctx := context.Background()
	tier := environment.TierProduction
	b := composed(bootstrap.Engine{Clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1"}, Records: fake.NewKeyValues(), Removable: tierParts()})
	usedPart(t, b, tier, bootstrap.RealtimePart, "shop")

	var refused refusal.Refusal
	if err := b.RefusePartsInUse(ctx, tier); !errors.As(err, &refused) || !strings.Contains(refused.Message, "shop") || !strings.Contains(refused.Message, "ocel bootstrap destroy production") {
		t.Errorf("RefusePartsInUse() while shop uses the realtime part = %v, want a refusal naming shop and the destroy to run again", err)
	}
	if _, err := b.PlanRemovedParts(ctx, tier, []string{bootstrap.RealtimePart}, true); !errors.As(err, &refused) || !strings.Contains(refused.Message, "--remove "+bootstrap.RealtimePart) {
		t.Errorf("PlanRemovedParts() of the realtime part while shop uses it = %v, want a refusal naming the removal to run again", err)
	}
	described, err := b.ListDescribedParts(ctx, tier)
	if err != nil || len(described) != 1 || !described[0].RetiresWithLastUser || !slices.Equal(described[0].UsedBy, []string{"shop"}) {
		t.Errorf("ListDescribedParts() = %+v, %v, want the realtime part used by shop and retiring with its last user", described, err)
	}

	if err := stackrecords.RecordPartsUsed(ctx, b.Records, tier, "shop", nil); err != nil {
		t.Fatal(err)
	}
	if err := b.RefusePartsInUse(ctx, tier); err != nil {
		t.Errorf("RefusePartsInUse() once no project uses the realtime part = %v, want it free to remove", err)
	}
	groups, err := b.PlanRemovedParts(ctx, tier, []string{bootstrap.RealtimePart}, false)
	if err != nil || len(groups) != 1 || groups[0].Action != provider.ActionDelete || len(groups[0].Changes) != 1 {
		t.Errorf("PlanRemovedParts() of the unused realtime part = %+v, %v, want its account deleted", groups, err)
	}
}

func TestTheKeysAreKeptWhileAnyProjectOfTheTierIsRecorded(t *testing.T) {
	ctx := context.Background()
	tier := environment.TierProduction
	b := composed(bootstrap.Engine{Clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1"}, Records: fake.NewKeyValues(), Removable: tierParts()})
	usedPart(t, b, tier, bootstrap.KeysPart)
	if _, err := b.Records.Write(ctx, records.Entry{Key: stackrecords.ProjectKey(tier, "docs"), Value: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}

	var refused refusal.Refusal
	if err := b.RefusePartsInUse(ctx, tier); !errors.As(err, &refused) || !strings.Contains(refused.Message, "docs") {
		t.Errorf("RefusePartsInUse() while docs is recorded = %v, want a refusal naming docs: its values are sealed under the keys whether its last deploy sealed one or not", err)
	}
	groups, err := b.ListPartRemovals(ctx, tier)
	if err != nil || len(groups) != 1 {
		t.Fatalf("ListPartRemovals() = %+v, %v, want the keys part", groups, err)
	}
	for _, change := range groups[0].Changes {
		if change.Kind == string(bootstrap.KindKeyRing) && change.Action != provider.ActionKeep {
			t.Errorf("removing the keys part plans %+v, want the ring kept: Google never deletes a key ring", change)
		}
	}
}
