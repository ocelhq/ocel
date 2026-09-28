package gcp_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"cloud.google.com/go/firestore"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
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
		t.Skipf("no floci-gcp emulator in the environment and %s names no project; run under `scripts/floci.sh --cloud gcp run <name> -- go test ./...`, or name a real project to run against",
			liveProjectVariable)
	}
	return newProvider(t, gcp.Options{Project: liveProject(), Region: liveRegion()})
}

func liveNames(t *testing.T) gcp.Names {
	t.Helper()
	return names(t, live(t))
}

func TestLiveCredentials(t *testing.T) {
	credentials := live(t).Credentials()

	conformance.RunCredentials(t, credentials)

	principal, err := credentials.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami() against the emulator = %v, want an identity: the project the run targets answers there", err)
	}
	if principal.Vendor != gcp.Vendor || principal.Account != liveProject() {
		t.Errorf("Whoami() = %+v, want %s naming project %s", principal, gcp.Vendor, liveProject())
	}
}

func TestLiveStacks(t *testing.T) {
	p := live(t)

	conformance.RunStacks(t, p.Facts(), p.Stacks(), p.Artifacts(), p.KeyValues())
}

func TestLiveTheKeyValueStoreConformsAsEveryStoreMust(t *testing.T) {
	conformance.RunStore(t, live(t).KeyValues())
}

func TestLiveASchemaTheOlderLayoutWroteIsRefusedRatherThanStampedOver(t *testing.T) {
	p := live(t)
	bootstrapped(t, p, environment.TierPreview)

	ctx := context.Background()
	client, err := firestore.NewClientWithDatabase(ctx, liveProject(), liveNames(t).Database(), ports.EmulatorGRPC(endpoint())...)
	if err != nil {
		t.Fatalf("open Firestore: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	older := ports.OpenTierCollection(client, environment.TierPreview).Doc("schema#preview")
	if _, err := older.Set(ctx, map[string]any{"body": []byte("2"), "rev": "older"}); err != nil {
		t.Fatalf("write the schema the older layout kept: %v", err)
	}
	t.Cleanup(func() { _, _ = older.Delete(ctx) })

	var refused refusal.Refusal
	err = stackrecords.EnsureSchema(ctx, p.KeyValues(), environment.TierPreview)
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("EnsureSchema() over a schema the older layout wrote = %v, want a %s refusal: a build that reads it as unwritten stamps its own schema beside records it cannot see", err, refusal.CodeNotReady)
	}
}

func TestLiveTheCipherSealsAsEveryCipherMust(t *testing.T) {
	vendor := live(t)
	bootstrapped(t, vendor, environment.TierProduction)

	conformance.RunCipher(t, vendor.Cipher())
}

func TestLiveSealingWhereNoKeyRingExistsSaysWhatToRun(t *testing.T) {
	live(t)

	elsewhere := newProvider(t, gcp.Options{Project: liveProject(), Region: "australia-southeast2"})
	var refused refusal.Refusal
	_, err := elsewhere.Cipher().Seal(context.Background(), environment.TierProduction, seal.AssociatedData{
		{Name: "project", Value: "shop"},
		{Name: "key", Value: "DATABASE_URL"},
	}, []byte("postgres://example"))
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Seal() where no key ring exists = %v, want a %s refusal", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, "ocel bootstrap") {
		t.Errorf("Seal() refused with %q, want it to name the command that creates the key", refused.Message)
	}
}

func bootstrappedTiers(t *testing.T, p *gcp.Provider) {
	t.Helper()
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		bootstrapped(t, p, tier)
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
	ref := provider.ArtifactRef{Tier: environment.TierProduction, Bucket: provider.StoreFunctions, Key: "conformance/bundle.zip"}

	var refused refusal.Refusal
	err := nowhere.Artifacts().Put(context.Background(), ref, bytes.NewReader([]byte("a build artifact")))
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Put() where no bucket exists = %v, want a %s refusal", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, "ocel bootstrap") {
		t.Errorf("Put() refused with %q, want it to name the command that creates the bucket", refused.Message)
	}
}

func TestLiveRemovingAPrefixLeavesEveryStoreItDoesNotName(t *testing.T) {
	p := live(t)
	bootstrappedTiers(t, p)

	ctx := context.Background()
	artifacts := p.Artifacts()
	tier := environment.TierProduction
	swept, kept := "conformance/"+t.Name()+"/", "conformance/"+t.Name()+"-beside/"

	for _, store := range []string{provider.StoreFunctions, provider.StoreAssets, provider.StoreCache} {
		for _, prefix := range []string{swept, kept} {
			ref := provider.ArtifactRef{Tier: tier, Bucket: store, Key: prefix + "bundle.zip"}
			if err := artifacts.Put(ctx, ref, bytes.NewReader([]byte(store))); err != nil {
				t.Fatalf("Put(%s/%s) = %v", store, prefix, err)
			}
		}
	}

	if err := artifacts.RemovePrefix(ctx, tier, swept, nil); err != nil {
		t.Fatalf("RemovePrefix(%s) = %v", swept, err)
	}

	for _, store := range []string{provider.StoreFunctions, provider.StoreAssets, provider.StoreCache} {
		gone, err := artifacts.Has(ctx, provider.ArtifactRef{Tier: tier, Bucket: store, Key: swept + "bundle.zip"})
		if err != nil || gone {
			t.Errorf("Has(%s/%s) = %v, %v, want it swept: one prefix names the same run in every store", store, swept, gone, err)
		}
		left, err := artifacts.Has(ctx, provider.ArtifactRef{Tier: tier, Bucket: store, Key: kept + "bundle.zip"})
		if err != nil || !left {
			t.Errorf("Has(%s/%s) = %v, %v, want it left alone", store, kept, left, err)
		}
	}
}

func TestLiveRemovingNoPrefixIsRefusedRatherThanSweepingTheBucket(t *testing.T) {
	p := live(t)
	bootstrappedTiers(t, p)

	ctx := context.Background()
	artifacts := p.Artifacts()
	ref := provider.ArtifactRef{Tier: environment.TierProduction, Bucket: provider.StoreFunctions, Key: "conformance/" + t.Name() + "/bundle.zip"}
	if err := artifacts.Put(ctx, ref, bytes.NewReader([]byte("a build artifact"))); err != nil {
		t.Fatal(err)
	}

	var refused refusal.Refusal
	err := artifacts.RemovePrefix(ctx, environment.TierProduction, "", nil)
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("RemovePrefix(\"\") = %v, want an %s refusal: an empty prefix names every artifact the tier keeps", err, refusal.CodeInvalid)
	}
	stored, err := artifacts.Has(ctx, ref)
	if err != nil || !stored {
		t.Fatalf("Has() after RemovePrefix(\"\") = %v, %v, want the artifact left where it was", stored, err)
	}
}
