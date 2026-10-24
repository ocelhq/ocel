//go:build integration

package gcp_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/postgres"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/realtime"
)

const (
	liveProjectVariable = "OCEL_GCP_LIVE_PROJECT"
	liveRegionVariable  = "OCEL_GCP_LIVE_REGION"

	emulatedProject = "floci-local"
	emulatedRegion  = "europe-west1"
)

func emulated() bool { return endpoint() != "" }

func liveProject() string {
	if project := os.Getenv(liveProjectVariable); project != "" {
		return project
	}
	return emulatedProject
}

func liveRegion() string {
	if region := os.Getenv(liveRegionVariable); region != "" {
		return region
	}
	return emulatedRegion
}

func live(t *testing.T) *gcp.Provider {
	t.Helper()
	if !emulated() && os.Getenv(liveProjectVariable) == "" {
		t.Fatalf("no floci-gcp emulator in the environment and %s names no project; run under `scripts/floci.sh --cloud gcp run <name> -- go test ./...`, or name a real project to run against",
			liveProjectVariable)
	}
	return newProvider(t, gcp.Options{Project: liveProject(), Region: liveRegion()})
}

func liveNames(t *testing.T) names.Names {
	t.Helper()
	return namesOf(t, live(t))
}

func TestLiveCredentials(t *testing.T) {
	credentials := live(t).Credentials()

	conformance.RunCredentials(t, credentials)

	principal, err := credentials.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami() against the emulator = %v, want an identity: the project the run targets answers there", err)
	}
	if principal.Vendor != gcloud.Vendor || principal.Account != liveProject() {
		t.Errorf("Whoami() = %+v, want %s naming project %s", principal, gcloud.Vendor, liveProject())
	}
}

func servableFacts(facts provider.Facts) provider.Facts {
	if emulated() {
		facts.Bindings = slices.DeleteFunc(slices.Clone(facts.Bindings), func(b provider.BindingType) bool { return b == provider.BindingKV })
	}
	return facts
}

func TestLiveStacks(t *testing.T) {
	p := live(t)
	parts := []string{bootstrap.EdgeBucketPart, bootstrap.CacheBucketPart, bootstrap.RealtimePart, bootstrap.TopicsPart}
	if !emulated() {
		parts = append(parts, gcp.NetworkPart)
	}
	bootstrappedWith(t, p, environment.TierPreview, parts...)
	projectDatabaseMade(t, p, environment.TierPreview, "conformance")

	conformance.RunStacks(t, servableFacts(p.Facts()), p.Stacks(), p.Artifacts(), p.KeyValues(), realtime.RootRealtime, postgres.RootPostgres)
}

func TestLiveTheKeyValueStoreConformsAsEveryStoreMust(t *testing.T) {
	conformance.RunStore(t, live(t).KeyValues())
}

func TestLiveTheCipherSealsAsEveryCipherMust(t *testing.T) {
	vendor := live(t)
	bootstrapped(t, vendor, environment.TierProduction)

	conformance.RunCipher(t, vendor.Cipher())
}

func TestLiveSealingInATierNeverBootstrappedSaysWhatToRun(t *testing.T) {
	live(t)

	nowhere := newProvider(t, gcp.Options{Project: "floci-nowhere", Region: liveRegion()})
	var refused refusal.Refusal
	_, err := nowhere.Cipher().Seal(context.Background(), environment.TierProduction, seal.BindToProject("shop", "variable", "DATABASE_URL"), []byte("postgres://example"))
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Seal() in a tier never bootstrapped = %v, want a %s refusal: the keys part a first seal adds is recorded in the tier's records database", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, "ocel bootstrap") {
		t.Errorf("Seal() refused with %q, want it to name the command that bootstraps the tier", refused.Message)
	}
}

func bootstrappedTiers(t *testing.T, p *gcp.Provider) {
	t.Helper()
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		bootstrappedWith(t, p, tier, bootstrap.EdgeBucketPart, bootstrap.CacheBucketPart)
	}
}

func TestLiveArtifactStore(t *testing.T) {
	vendor := live(t)
	bootstrappedTiers(t, vendor)

	conformance.RunArtifactStore(t, vendor.Facts(), vendor.Artifacts())
}

func TestLiveArtifactsWhereNoBucketExistsSayWhatToRun(t *testing.T) {
	live(t)

	nowhere := newProvider(t, gcp.Options{Project: "floci-nowhere", Region: liveRegion()})
	ref := provider.ArtifactRef{Tier: environment.TierProduction, Key: "assets/conformance/prod/web/r1a2b3c4d/bundle.js"}

	var refused refusal.Refusal
	err := nowhere.Artifacts().Put(context.Background(), ref, bytes.NewReader([]byte("a build artifact")))
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Put() where no bucket exists = %v, want a %s refusal", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, "ocel bootstrap") {
		t.Errorf("Put() refused with %q, want it to name the command that creates the bucket", refused.Message)
	}
}

func TestLiveRemovingAScopeLeavesEveryKindItDoesNotName(t *testing.T) {
	p := live(t)
	bootstrappedTiers(t, p)

	ctx := context.Background()
	artifacts := p.Artifacts()
	tier := environment.TierProduction
	swept := naming.Coordinate{Project: "conformance", Env: "prod", App: "web", Release: naming.NewReleaseToken(t.Name(), "")}
	beside := swept
	beside.Release = naming.NewReleaseToken(t.Name()+"-beside", "")
	kinds := []naming.ObjectKind{naming.ObjectFetchCache, naming.ObjectAssets, naming.ObjectPages}

	for _, kind := range append(kinds, naming.ObjectTagClock) {
		for _, release := range []naming.Coordinate{swept, beside} {
			ref := provider.ArtifactRef{Tier: tier, Key: release.ObjectKey(kind, "bundle.zip")}
			if err := artifacts.Put(ctx, ref, bytes.NewReader([]byte(kind))); err != nil {
				t.Fatalf("Put(%s) = %v", ref.Key, err)
			}
		}
	}

	if err := artifacts.RemoveObjects(ctx, tier, scopeOf(t)(naming.ReleaseScope(swept)), kinds, nil); err != nil {
		t.Fatalf("RemoveObjects(%+v) = %v", swept, err)
	}

	for _, kind := range kinds {
		key := swept.ObjectKey(kind, "bundle.zip")
		if gone, err := artifacts.Has(ctx, provider.ArtifactRef{Tier: tier, Key: key}); err != nil || gone {
			t.Errorf("Has(%s) = %v, %v, want it swept", key, gone, err)
		}
	}
	for _, key := range []string{swept.ObjectKey(naming.ObjectTagClock, "bundle.zip"), beside.ObjectKey(naming.ObjectFetchCache, "bundle.zip")} {
		if left, err := artifacts.Has(ctx, provider.ArtifactRef{Tier: tier, Key: key}); err != nil || !left {
			t.Errorf("Has(%s) = %v, %v, want it left alone", key, left, err)
		}
	}
	t.Cleanup(func() {
		_ = artifacts.RemoveObjects(ctx, tier, scopeOf(t)(naming.ProjectScope("conformance")), naming.CoordinateObjectKinds, nil)
	})
}

func TestLiveRemovingAScopeNamingNoProjectIsRefusedRatherThanSweepingTheBucket(t *testing.T) {
	p := live(t)
	bootstrappedTiers(t, p)

	ctx := context.Background()
	artifacts := p.Artifacts()
	release := naming.Coordinate{Project: "conformance", Env: "prod", App: "web", Name: "api", Release: naming.NewReleaseToken(t.Name(), "")}
	ref := provider.ArtifactRef{Tier: environment.TierProduction, Key: release.AssetKey("bundle.js")}
	if err := artifacts.Put(ctx, ref, bytes.NewReader([]byte("a build artifact"))); err != nil {
		t.Fatal(err)
	}

	var refused refusal.Refusal
	err := artifacts.RemoveObjects(ctx, environment.TierProduction, naming.StorageScope{}, naming.CoordinateObjectKinds, nil)
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("RemoveObjects() of an empty scope = %v, want an %s refusal: it names every project's objects", err, refusal.CodeInvalid)
	}
	stored, err := artifacts.Has(ctx, ref)
	if err != nil || !stored {
		t.Fatalf("Has() after RemoveObjects() of an empty scope = %v, %v, want the artifact left where it was", stored, err)
	}
}
