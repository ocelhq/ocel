package cloudfront

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	"github.com/ocelhq/ocel/pkg/providerkit/ledger"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	previewBase    = "preview.example.com"
	previewWild    = "*." + previewBase
	previewCert    = "arn:aws:acm:us-east-1:123456789012:certificate/preview"
	previewPointer = "pr1"
)

func previewStackSpec() edge.StackSpec {
	return edge.StackSpec{Version: "v1", Class: edge.ClassPreview, Slug: conformanceSlug, PruneOnly: true}
}

func previewWildcardSpec() edge.PreviewWildcardSpec {
	return edge.PreviewWildcardSpec{
		Version:     "v1",
		BaseDomain:  previewBase,
		Certificate: previewCert,
		GrammarMin:  edge.PreviewGrammarMin,
		GrammarMax:  edge.PreviewGrammarMax,
	}
}

func previewHostname() string {
	return edge.SharedPreview(conformanceSlug, previewBase).Host(previewPointer, "")
}

func previewBootstrapped(t *testing.T, w *world) *provider {
	t.Helper()
	e := w.edge()
	if _, err := e.Bootstrap(context.Background(), edge.ClassPreview); err != nil {
		t.Fatalf("Bootstrap(preview): %v", err)
	}
	return e
}

func previewing(t *testing.T, w *world) (*provider, edge.EdgeStack) {
	t.Helper()
	ctx := context.Background()
	e := previewBootstrapped(t, w)
	if _, err := e.ReconcilePreviewWildcard(ctx, previewWildcardSpec()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}
	stack, err := e.Reconcile(ctx, previewStackSpec(), edge.StackState{GlobalPreview: previewBase})
	if err != nil {
		t.Fatalf("Reconcile(preview): %v", err)
	}
	return e, stack
}

func previewRoutes(t *testing.T, w *world) map[string]route {
	t.Helper()
	arn := fakeRoutesARN(edge.ClassPreview)
	held := map[string]route{}
	for key, value := range w.store.held(arn) {
		var published route
		if err := json.Unmarshal([]byte(value), &published); err != nil {
			t.Fatalf("decode the route under %q: %v", key, err)
		}
		held[key] = published
	}
	return held
}

func promotePreview(t *testing.T, stack edge.EdgeStack, pointer string) {
	t.Helper()
	ctx := context.Background()
	staged(t, stack, fakeEntryURL, fakeAssetPrefix)
	if err := stack.Promote(ctx, edge.Promotion{
		PromotionID: "preview-" + pointer,
		Ts:          1,
		Builds:      map[string]string{"web": "d1.f1"},
	}, pointer, edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote(%s): %v", pointer, err)
	}
}

func TestReconcilePreviewWildcard(t *testing.T) {
	t.Run("one distribution carries the wildcard, its certificate and the preview resolver", func(t *testing.T) {
		w := newWorld()
		front, err := previewBootstrapped(t, w).ReconcilePreviewWildcard(context.Background(), previewWildcardSpec())
		if err != nil {
			t.Fatalf("ReconcilePreviewWildcard: %v", err)
		}

		held := w.front.named(previewWildcardName(previewBase))
		if held == nil {
			t.Fatalf("no distribution named %q; the account holds %v", previewWildcardName(previewBase), w.front.mutations())
		}
		if front != held.domain {
			t.Errorf("front = %q, want the distribution's domain name %q", front, held.domain)
		}
		if aliases := held.config.Aliases.Items; !slices.Equal(aliases, []string{previewWild}) {
			t.Errorf("aliases = %v, want exactly %q", aliases, previewWild)
		}
		if arn := aws.ToString(held.config.ViewerCertificate.ACMCertificateArn); arn != previewCert {
			t.Errorf("viewer certificate = %q, want the wildcard certificate %q", arn, previewCert)
		}
		associated := held.config.DefaultCacheBehavior.FunctionAssociations
		if associated == nil || len(associated.Items) != 1 {
			t.Fatalf("function associations = %+v, want the resolver attached at creation", associated)
		}
		if associated.Items[0].EventType != cftypes.EventTypeViewerRequest {
			t.Errorf("function association = %s, want it on the viewer request", associated.Items[0].EventType)
		}
		if arn := aws.ToString(associated.Items[0].FunctionARN); arn != fakeResolverARN(edge.ClassPreview) {
			t.Errorf("function association = %q, want the preview resolver", arn)
		}
		if origin := aws.ToString(held.config.Origins.Items[0].DomainName); origin != assetOriginDomain(fakeAssetBucket, fakeRegion) {
			t.Errorf("origin = %q, want the preview bootstrap's asset bucket", origin)
		}
	})

	t.Run("reconciling an unchanged wildcard changes nothing", func(t *testing.T) {
		w := newWorld()
		e := previewBootstrapped(t, w)
		ctx := context.Background()
		if _, err := e.ReconcilePreviewWildcard(ctx, previewWildcardSpec()); err != nil {
			t.Fatalf("ReconcilePreviewWildcard: %v", err)
		}
		w.front.calls = nil
		if _, err := e.ReconcilePreviewWildcard(ctx, previewWildcardSpec()); err != nil {
			t.Fatalf("ReconcilePreviewWildcard again: %v", err)
		}
		if made := w.front.mutations(); len(made) != 0 {
			t.Errorf("second reconcile changed %v, want it to find the distribution it already raised", made)
		}
		if n := len(w.front.distributions); n != 1 {
			t.Errorf("distributions = %d, want the one wildcard", n)
		}
	})

	t.Run("a re-issued certificate moves the wildcard onto it", func(t *testing.T) {
		w := newWorld()
		e := previewBootstrapped(t, w)
		ctx := context.Background()
		if _, err := e.ReconcilePreviewWildcard(ctx, previewWildcardSpec()); err != nil {
			t.Fatalf("ReconcilePreviewWildcard: %v", err)
		}
		raised := w.front.named(previewWildcardName(previewBase))

		spec := previewWildcardSpec()
		spec.Certificate = "arn:aws:acm:us-east-1:123456789012:certificate/reissued"
		front, err := e.ReconcilePreviewWildcard(ctx, spec)
		if err != nil {
			t.Fatalf("ReconcilePreviewWildcard onto the re-issued certificate: %v", err)
		}
		if front != raised.domain {
			t.Errorf("front = %q, want the distribution already serving %q", front, previewWild)
		}
		if n := len(w.front.distributions); n != 1 {
			t.Errorf("distributions = %d, want the one wildcard converged rather than a second raised", n)
		}
		held := w.front.named(previewWildcardName(previewBase))
		if arn := aws.ToString(held.config.ViewerCertificate.ACMCertificateArn); arn != spec.Certificate {
			t.Errorf("viewer certificate = %q, want the re-issued certificate %q", arn, spec.Certificate)
		}
		if aliases := held.config.Aliases.Items; !slices.Equal(aliases, []string{previewWild}) {
			t.Errorf("aliases = %v, want exactly %q", aliases, previewWild)
		}
	})

	t.Run("a wildcard that lost its alias gets it back", func(t *testing.T) {
		w := newWorld()
		e := previewBootstrapped(t, w)
		ctx := context.Background()
		if _, err := e.ReconcilePreviewWildcard(ctx, previewWildcardSpec()); err != nil {
			t.Fatalf("ReconcilePreviewWildcard: %v", err)
		}
		raised := w.front.named(previewWildcardName(previewBase))
		raised.config.Aliases = &cftypes.Aliases{Quantity: aws.Int32(0)}

		if _, err := e.ReconcilePreviewWildcard(ctx, previewWildcardSpec()); err != nil {
			t.Fatalf("ReconcilePreviewWildcard again: %v", err)
		}
		held := w.front.named(previewWildcardName(previewBase))
		if aliases := held.config.Aliases.Items; !slices.Equal(aliases, []string{previewWild}) {
			t.Errorf("aliases = %v, want %q served again", aliases, previewWild)
		}
	})

	t.Run("a certificate CloudFront cannot read is refused by name", func(t *testing.T) {
		spec := previewWildcardSpec()
		spec.Certificate = "arn:aws:acm:eu-west-1:123456789012:certificate/preview"
		_, err := previewBootstrapped(t, newWorld()).ReconcilePreviewWildcard(context.Background(), spec)
		if err == nil {
			t.Fatal("ReconcilePreviewWildcard with a eu-west-1 certificate err = nil, want a refusal")
		}
		for _, want := range []string{"us-east-1", "eu-west-1"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("a distribution another run raised under the same name is adopted", func(t *testing.T) {
		w := newWorld()
		e := previewBootstrapped(t, w)
		w.front.createDistributionErr = &cftypes.DistributionAlreadyExists{Message: aws.String("another run got there first")}
		w.front.distributions["E9"] = &fakeDistribution{
			id:     "E9",
			domain: "e9.cloudfront.net",
			etag:   "dist-9",
			config: &cftypes.DistributionConfig{Comment: aws.String(previewWildcardName(previewBase))},
		}

		front, err := e.ReconcilePreviewWildcard(context.Background(), previewWildcardSpec())
		if err != nil {
			t.Fatalf("ReconcilePreviewWildcard: %v", err)
		}
		if front != "e9.cloudfront.net" {
			t.Errorf("front = %q, want the distribution the other run raised", front)
		}
		held := w.front.distributions["E9"]
		if aliases := held.config.Aliases.Items; !slices.Equal(aliases, []string{previewWild}) {
			t.Errorf("aliases = %v, want the adopted distribution moved onto %q", aliases, previewWild)
		}
		if arn := aws.ToString(held.config.ViewerCertificate.ACMCertificateArn); arn != previewCert {
			t.Errorf("viewer certificate = %q, want the wildcard certificate %q", arn, previewCert)
		}
	})

	t.Run("without a base domain, a certificate or a bootstrap there is nothing to raise", func(t *testing.T) {
		ctx := context.Background()

		spec := previewWildcardSpec()
		spec.BaseDomain = ""
		if _, err := previewBootstrapped(t, newWorld()).ReconcilePreviewWildcard(ctx, spec); err == nil {
			t.Error("ReconcilePreviewWildcard without a base domain err = nil, want an error")
		}

		spec = previewWildcardSpec()
		spec.Certificate = ""
		if _, err := previewBootstrapped(t, newWorld()).ReconcilePreviewWildcard(ctx, spec); err == nil {
			t.Error("ReconcilePreviewWildcard without a certificate err = nil, want an error")
		}

		w := newWorld()
		raised := previewBootstrapped(t, w)
		w.cfn.absent = true
		if _, err := raised.ReconcilePreviewWildcard(ctx, previewWildcardSpec()); err == nil {
			t.Error("ReconcilePreviewWildcard without a preview bootstrap err = nil, want an error")
		}

		fronted := newWorld()
		fronted.cfn.otherEdge = true
		if _, err := fronted.edge().ReconcilePreviewWildcard(ctx, previewWildcardSpec()); err == nil {
			t.Error("ReconcilePreviewWildcard without a bootstrapped edge set err = nil, want an error")
		}
	})
}

func TestTheWildcardIsAFrontTheTagInvalidatorReaches(t *testing.T) {
	t.Run("reconciling it names it once for every project it fronts", func(t *testing.T) {
		w := newWorld()
		if _, err := previewBootstrapped(t, w).ReconcilePreviewWildcard(context.Background(), previewWildcardSpec()); err != nil {
			t.Fatalf("ReconcilePreviewWildcard: %v", err)
		}

		raised := w.front.named(previewWildcardName(previewBase))
		held := w.invalidationTargets(ledger.Scope(edge.ClassPreview, ""))
		if !slices.Equal(held, []string{raised.id}) {
			t.Errorf("bootstrap invalidation targets = %v, want the wildcard every preview is served from (%q)", held, raised.id)
		}
		if perProject := w.invalidationTargets(ledger.Scope(edge.ClassPreview, conformanceSlug)); perProject != nil {
			t.Errorf("project invalidation targets = %v, want a shared front named once rather than per project", perProject)
		}
	})

	t.Run("destroying it takes the name back", func(t *testing.T) {
		w := newWorld()
		e := previewBootstrapped(t, w)
		ctx := context.Background()
		if _, err := e.ReconcilePreviewWildcard(ctx, previewWildcardSpec()); err != nil {
			t.Fatalf("ReconcilePreviewWildcard: %v", err)
		}
		if err := e.DestroyPreviewWildcard(ctx, previewBase); err != nil {
			t.Fatalf("DestroyPreviewWildcard: %v", err)
		}
		if held := w.invalidationTargets(ledger.Scope(edge.ClassPreview, "")); len(held) != 0 {
			t.Errorf("bootstrap invalidation targets = %v, want a torn-down wildcard invalidated by nobody", held)
		}
	})
}

func TestDestroyPreviewWildcard(t *testing.T) {
	t.Run("the distribution is disabled, then deleted", func(t *testing.T) {
		w := newWorld()
		e := previewBootstrapped(t, w)
		ctx := context.Background()
		if _, err := e.ReconcilePreviewWildcard(ctx, previewWildcardSpec()); err != nil {
			t.Fatalf("ReconcilePreviewWildcard: %v", err)
		}
		raised := w.front.named(previewWildcardName(previewBase))
		w.front.calls = nil

		if err := e.DestroyPreviewWildcard(ctx, previewBase); err != nil {
			t.Fatalf("DestroyPreviewWildcard: %v", err)
		}
		want := []string{"UpdateDistribution " + raised.id, "DeleteDistribution " + raised.id}
		if made := w.front.mutations(); !slices.Equal(made, want) {
			t.Errorf("calls = %v, want %v", made, want)
		}
		if w.front.named(previewWildcardName(previewBase)) != nil {
			t.Error("the wildcard distribution is still there after DestroyPreviewWildcard")
		}
	})

	t.Run("destroying a wildcard nothing raised changes nothing", func(t *testing.T) {
		w := newWorld()
		e := previewBootstrapped(t, w)
		w.front.calls = nil
		if err := e.DestroyPreviewWildcard(context.Background(), previewBase); err != nil {
			t.Fatalf("DestroyPreviewWildcard: %v", err)
		}
		if made := w.front.mutations(); len(made) != 0 {
			t.Errorf("calls = %v, want nothing changed", made)
		}
	})

	t.Run("every project's preview keys on the base domain go with it", func(t *testing.T) {
		w := newWorld()
		e, stack := previewing(t, w)
		promotePreview(t, stack, previewPointer)
		promotePreview(t, stack, "pr2")
		arn := fakeRoutesARN(edge.ClassPreview)
		w.store.items[arn]["other-project--pr7."+previewBase] = `{"origin":"stale"}`
		w.store.items[arn]["www.unrelated.example"] = `{"origin":"kept"}`
		w.store.listPage = 1

		if err := e.DestroyPreviewWildcard(context.Background(), previewBase); err != nil {
			t.Fatalf("DestroyPreviewWildcard: %v", err)
		}
		held := slices.Sorted(maps.Keys(w.store.held(arn)))
		if !slices.Equal(held, []string{"www.unrelated.example"}) {
			t.Errorf("keys = %v, want every hostname on %q swept and nothing else", held, previewBase)
		}
	})

	t.Run("a base domain no one named is nothing to destroy", func(t *testing.T) {
		if err := newWorld().edge().DestroyPreviewWildcard(context.Background(), ""); err != nil {
			t.Fatalf("DestroyPreviewWildcard(\"\"): %v", err)
		}
	})
}

func TestPreviewPromoteWritesTheHostnameKey(t *testing.T) {
	t.Run("a preview promotion is a key on the hostname, not a distribution", func(t *testing.T) {
		w := newWorld()
		_, stack := previewing(t, w)
		w.front.calls = nil

		promotePreview(t, stack, previewPointer)

		held := previewRoutes(t, w)
		published, ok := held[previewHostname()]
		if !ok {
			t.Fatalf("routes = %v, want one under %q", slices.Sorted(maps.Keys(held)), previewHostname())
		}
		if published.Origin != fakeEntryHost || published.Release != "d1.f1" {
			t.Errorf("route = %+v, want the release's entry function URL", published)
		}
		if published.Assets != assetOriginDomain(fakeAssetBucket, fakeRegion) || published.AssetPrefix != "/"+fakeAssetPrefix {
			t.Errorf("route = %+v, want the preview bootstrap's assets", published)
		}
		if published.Secret != fakeSecret {
			t.Errorf("route = %+v, want the origin secret the entry function demands", published)
		}
		if made := w.front.mutations(); len(made) != 0 {
			t.Errorf("promoting a preview changed %v, want the wildcard distribution left alone", made)
		}
	})

	t.Run("the write is conditional on the version it read", func(t *testing.T) {
		w := newWorld()
		_, stack := previewing(t, w)
		w.store.conflicts = 1

		promotePreview(t, stack, previewPointer)

		if _, ok := previewRoutes(t, w)[previewHostname()]; !ok {
			t.Error("the preview hostname carries no route after a conflicting writer moved the store")
		}
		if n := w.store.count("kvs.UpdateKeys"); n != 2 {
			t.Errorf("UpdateKeys = %d, want the conflicted write retried once", n)
		}
	})

	t.Run("a store that refuses the write records no promotion", func(t *testing.T) {
		w := newWorld()
		_, stack := previewing(t, w)
		w.store.updateErr = errors.New("the store is closed")

		staged(t, stack, fakeEntryURL, fakeAssetPrefix)
		if err := stack.Promote(context.Background(), edge.Promotion{
			PromotionID: "refused",
			Ts:          1,
			Builds:      map[string]string{"web": "d1.f1"},
		}, previewPointer, edge.DiscardReporter()); err == nil {
			t.Fatal("Promote err = nil, want the refusal from the key value store")
		}
		history, err := stack.Ledger().History(context.Background(), previewPointer)
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(history) != 0 {
			t.Errorf("history = %v, want nothing recorded when the hostname was never published", history)
		}
	})

	t.Run("removing the pointer takes the hostname key with it", func(t *testing.T) {
		w := newWorld()
		_, stack := previewing(t, w)
		promotePreview(t, stack, previewPointer)

		if _, err := stack.RemovePointer(context.Background(), previewPointer); err != nil {
			t.Fatalf("RemovePointer: %v", err)
		}
		if held := previewRoutes(t, w); len(held) != 0 {
			t.Errorf("routes = %v, want none once the preview is gone", slices.Sorted(maps.Keys(held)))
		}
	})

	t.Run("destroying the stack takes every preview it published", func(t *testing.T) {
		w := newWorld()
		_, stack := previewing(t, w)
		promotePreview(t, stack, previewPointer)
		promotePreview(t, stack, "pr2")
		w.front.calls = nil

		if err := stack.Destroy(context.Background()); err != nil {
			t.Fatalf("Destroy: %v", err)
		}
		if held := previewRoutes(t, w); len(held) != 0 {
			t.Errorf("routes = %v, want none after the stack was destroyed", slices.Sorted(maps.Keys(held)))
		}
		if slices.Contains(w.front.calls, "ListDistributions") {
			t.Error("destroying a stack that serves on the wildcard listed the account's distributions")
		}
	})

	t.Run("a stack that moved onto its own preview domain still withdraws what it published", func(t *testing.T) {
		w := newWorld()
		e, previewed := previewing(t, w)
		promotePreview(t, previewed, previewPointer)

		reconciled, err := e.Reconcile(context.Background(), previewStackSpec(), previewed.State())
		if err != nil {
			t.Fatalf("Reconcile once the project declares its own preview domain: %v", err)
		}
		moved := reconciled.(*stack)
		moved.state.GlobalPreview = ""

		if err := moved.Destroy(context.Background()); err != nil {
			t.Fatalf("Destroy: %v", err)
		}
		if held := previewRoutes(t, w); len(held) != 0 {
			t.Errorf("routes = %v, want the wildcard hostnames withdrawn even once the stack stopped declaring the base", slices.Sorted(maps.Keys(held)))
		}
	})

	t.Run("a destroy that cannot withdraw the hostnames keeps the ledger that names them", func(t *testing.T) {
		w := newWorld()
		_, stack := previewing(t, w)
		promotePreview(t, stack, previewPointer)
		w.store.updateErr = errors.New("the store is closed")

		if err := stack.Destroy(context.Background()); err == nil {
			t.Fatal("Destroy err = nil, want the refusal from the key value store")
		}
		w.store.updateErr = nil
		if err := stack.Destroy(context.Background()); err != nil {
			t.Fatalf("Destroy again: %v", err)
		}
		if held := previewRoutes(t, w); len(held) != 0 {
			t.Errorf("routes = %v, want the re-run to find the pointers it needed and withdraw them", slices.Sorted(maps.Keys(held)))
		}
	})

	t.Run("a ledger that refuses a first promotion leaves no hostname behind", func(t *testing.T) {
		w := newWorld()
		_, stack := previewing(t, w)
		staged(t, stack, fakeEntryURL, fakeAssetPrefix)
		w.dynamo.putErr = errors.New("the table is closed")

		if err := stack.Promote(context.Background(), edge.Promotion{
			PromotionID: "orphan",
			Ts:          1,
			Builds:      map[string]string{"web": "d1.f1"},
		}, previewPointer, edge.DiscardReporter()); err == nil {
			t.Fatal("Promote err = nil, want the refusal from the deployments ledger")
		}
		if held := previewRoutes(t, w); len(held) != 0 {
			t.Errorf("routes = %v, want no hostname left pointing at a release the ledger never recorded", slices.Sorted(maps.Keys(held)))
		}
	})
}
