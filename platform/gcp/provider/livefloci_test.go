package gcp_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
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

func TestLiveReleaser(t *testing.T) {
	p := live(t)

	conformance.RunStacks(t, p.Facts(), p.Stacks(), p.Artifacts(), p.Records())
}

func TestLiveRecordStore(t *testing.T) {
	conformance.RunStore(t, live(t).Records())
}

func TestLiveRecordNamesSurviveTheCharactersTheDocumentIdIsBuiltFrom(t *testing.T) {
	ctx := context.Background()
	store := live(t).Records()

	for _, segment := range []string{"a#b", "a%b", "a/b", "a%23b"} {
		name := records.Name{records.RootConformance, string(edge.ClassProduction), t.Name(), segment}
		if _, err := store.Write(ctx, records.Record{Name: name, Bytes: []byte(segment)}); err != nil {
			t.Fatalf("Write(%s) = %v", name, err)
		}
	}

	under := records.Name{records.RootConformance, string(edge.ClassProduction), t.Name()}
	held, err := store.List(ctx, under)
	if err != nil {
		t.Fatalf("List(%s) = %v", under, err)
	}
	if len(held) != 4 {
		t.Fatalf("List(%s) returned %d records, want the 4 written under it: a name a document id cannot hold collides with its neighbours", under, len(held))
	}
	for _, record := range held {
		if len(record.Name) != 4 {
			t.Errorf("List() returned %v, want the four segments written", record.Name)
			continue
		}
		if !bytes.Equal(record.Bytes, []byte(record.Name[3])) {
			t.Errorf("List() returned %s carrying %q, want the segment read back as it was written", record.Name, record.Bytes)
		}
	}
}

func TestLiveSealer(t *testing.T) {
	provider := live(t)
	bootstrapped(t, provider, edge.ClassProduction)

	conformance.RunCipher(t, provider.Cipher())
}

func TestLiveSealingWhereNoKeyRingStandsSaysWhatToRun(t *testing.T) {
	live(t)

	elsewhere := newProvider(t, gcp.Options{Project: liveProject(), Region: "australia-southeast2"})
	var refused refusal.Refusal
	_, err := elsewhere.Cipher().Seal(context.Background(), records.SealScope{
		Project: "shop",
		Class:   edge.ClassProduction,
		Env:     "*",
		Folder:  "/",
		Name:    "DATABASE_URL",
	}, []byte("postgres://example"))
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Seal() where no key ring stands = %v, want a %s refusal", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, "ocel bootstrap") {
		t.Errorf("Seal() refused with %q, want it to name the command that creates the key", refused.Message)
	}
}

func bucketsStanding(t *testing.T, p *gcp.Provider) {
	t.Helper()
	for _, class := range []edge.Class{edge.ClassProduction, edge.ClassPreview} {
		bootstrapped(t, p, class)
	}
}

func TestLiveArtifactStore(t *testing.T) {
	provider := live(t)
	bucketsStanding(t, provider)

	conformance.RunArtifactStore(t, provider.Facts(), provider.Artifacts())
}

func TestLiveArtifactsWhereNoBucketStandsSayWhatToRun(t *testing.T) {
	live(t)

	nowhere := newProvider(t, gcp.Options{Project: "floci-nowhere", Region: liveRegion()})
	ref := provider.ArtifactRef{Class: edge.ClassProduction, Bucket: provider.StoreFunctions, Key: "conformance/bundle.zip"}

	var refused refusal.Refusal
	err := nowhere.Artifacts().Put(context.Background(), ref, bytes.NewReader([]byte("a build artifact")))
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Put() where no bucket stands = %v, want a %s refusal", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, "ocel bootstrap") {
		t.Errorf("Put() refused with %q, want it to name the command that creates the bucket", refused.Message)
	}
}

func TestLiveRemovingAPrefixLeavesEveryStoreItDoesNotName(t *testing.T) {
	p := live(t)
	bucketsStanding(t, p)

	ctx := context.Background()
	artifacts := p.Artifacts()
	class := edge.ClassProduction
	swept, kept := "conformance/"+t.Name()+"/", "conformance/"+t.Name()+"-beside/"

	for _, store := range []string{provider.StoreFunctions, provider.StoreAssets, provider.StoreCache} {
		for _, prefix := range []string{swept, kept} {
			ref := provider.ArtifactRef{Class: class, Bucket: store, Key: prefix + "bundle.zip"}
			if err := artifacts.Put(ctx, ref, bytes.NewReader([]byte(store))); err != nil {
				t.Fatalf("Put(%s/%s) = %v", store, prefix, err)
			}
		}
	}

	if err := artifacts.RemovePrefix(ctx, class, swept, nil); err != nil {
		t.Fatalf("RemovePrefix(%s) = %v", swept, err)
	}

	for _, store := range []string{provider.StoreFunctions, provider.StoreAssets, provider.StoreCache} {
		gone, err := artifacts.Has(ctx, provider.ArtifactRef{Class: class, Bucket: store, Key: swept + "bundle.zip"})
		if err != nil || gone {
			t.Errorf("Has(%s/%s) = %v, %v, want it swept: one prefix names the same run in every store", store, swept, gone, err)
		}
		held, err := artifacts.Has(ctx, provider.ArtifactRef{Class: class, Bucket: store, Key: kept + "bundle.zip"})
		if err != nil || !held {
			t.Errorf("Has(%s/%s) = %v, %v, want it left alone", store, kept, held, err)
		}
	}
}

func TestLiveRemovingNoPrefixIsRefusedRatherThanSweepingTheBucket(t *testing.T) {
	held := live(t)
	bucketsStanding(t, held)

	ctx := context.Background()
	artifacts := held.Artifacts()
	ref := provider.ArtifactRef{Class: edge.ClassProduction, Bucket: provider.StoreFunctions, Key: "conformance/" + t.Name() + "/bundle.zip"}
	if err := artifacts.Put(ctx, ref, bytes.NewReader([]byte("a build artifact"))); err != nil {
		t.Fatal(err)
	}

	var refused refusal.Refusal
	err := artifacts.RemovePrefix(ctx, edge.ClassProduction, "", nil)
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("RemovePrefix(\"\") = %v, want an %s refusal: an empty prefix names every artifact the class keeps", err, refusal.CodeInvalid)
	}
	stored, err := artifacts.Has(ctx, ref)
	if err != nil || !stored {
		t.Fatalf("Has() after RemovePrefix(\"\") = %v, %v, want the artifact left where it was", stored, err)
	}
}

func TestLiveListingUnderANameReturnsTheRecordStoredAtIt(t *testing.T) {
	ctx := context.Background()
	store := live(t).Records()

	under := records.Name{records.RootConformance, string(edge.ClassProduction), t.Name()}
	for _, name := range []records.Name{under, append(slices.Clone(under), "beneath")} {
		if _, err := store.Write(ctx, records.Record{Name: name, Bytes: []byte(name.String())}); err != nil {
			t.Fatalf("Write(%s) = %v", name, err)
		}
	}

	held, err := store.List(ctx, under)
	if err != nil {
		t.Fatalf("List(%s) = %v", under, err)
	}
	if len(held) != 2 {
		t.Fatalf("List(%s) returned %d records, want the record at the name and the one beneath it: the sibling store answers with both", under, len(held))
	}
}
