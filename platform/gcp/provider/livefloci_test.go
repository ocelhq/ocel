package gcp_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

const (
	liveProject = "floci-local"
	liveRegion  = "europe-west1"
)

func live(t *testing.T) *gcp.Provider {
	t.Helper()
	if os.Getenv("OCEL_FLOCI_GCP_ENDPOINT") == "" {
		t.Skip("no floci-gcp emulator in the environment; run under `scripts/floci.sh --cloud gcp run <name> -- go test ./...`")
	}
	return newProvider(t, gcp.Options{Project: liveProject, Region: liveRegion})
}

func TestLiveCredentials(t *testing.T) {
	credentials := live(t).Credentials()

	conformance.RunCredentials(t, credentials)

	identity, err := credentials.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami() against the emulator = %v, want an identity: the project the run targets answers there", err)
	}
	if identity.Provider != gcp.Vendor || identity.Account != liveProject {
		t.Errorf("Whoami() = %+v, want %s naming project %s", identity, gcp.Vendor, liveProject)
	}
}

func TestLiveRecordStore(t *testing.T) {
	conformance.RunRecordStore(t, live(t).Records())
}

func TestLiveRecordNamesSurviveTheCharactersTheDocumentIdIsBuiltFrom(t *testing.T) {
	ctx := context.Background()
	records := live(t).Records()

	for _, segment := range []string{"a#b", "a%b", "a/b", "a%23b"} {
		name := providerkit.RecordName{providerkit.RootConformance, string(providerkit.ClassProduction), t.Name(), segment}
		if _, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte(segment)}); err != nil {
			t.Fatalf("Write(%s) = %v", name, err)
		}
	}

	under := providerkit.RecordName{providerkit.RootConformance, string(providerkit.ClassProduction), t.Name()}
	held, err := records.List(ctx, under)
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
	bootstrapped(t, provider, providerkit.ClassProduction)

	conformance.RunSealer(t, provider.Sealer())
}

func TestLiveSealingWhereNoKeyRingStandsSaysWhatToRun(t *testing.T) {
	live(t)

	elsewhere := newProvider(t, gcp.Options{Project: liveProject, Region: "australia-southeast2"})
	var refusal providerkit.Refusal
	_, err := elsewhere.Sealer().Seal(context.Background(), providerkit.Coordinate{
		Project: "shop",
		Class:   providerkit.ClassProduction,
		Env:     "*",
		Folder:  "/",
		Name:    "DATABASE_URL",
	}, []byte("postgres://example"))
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeNotReady {
		t.Fatalf("Seal() where no key ring stands = %v, want a %s refusal", err, providerkit.CodeNotReady)
	}
	if !strings.Contains(refusal.Message, "ocel bootstrap") {
		t.Errorf("Seal() refused with %q, want it to name the command that creates the key", refusal.Message)
	}
}

func bucketsStanding(t *testing.T, p *gcp.Provider) {
	t.Helper()
	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		bootstrapped(t, p, class)
	}
}

func TestLiveArtifactStore(t *testing.T) {
	provider := live(t)
	bucketsStanding(t, provider)

	conformance.RunArtifactStore(t, provider.Artifacts())
}

func TestLiveArtifactsWhereNoBucketStandsSayWhatToRun(t *testing.T) {
	live(t)

	nowhere := newProvider(t, gcp.Options{Project: "floci-nowhere", Region: liveRegion})
	ref := providerkit.ArtifactRef{Class: providerkit.ClassProduction, Bucket: providerkit.StoreFunctions, Key: "conformance/bundle.zip"}

	var refusal providerkit.Refusal
	err := nowhere.Artifacts().Put(context.Background(), ref, bytes.NewReader([]byte("a build artifact")))
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeNotReady {
		t.Fatalf("Put() where no bucket stands = %v, want a %s refusal", err, providerkit.CodeNotReady)
	}
	if !strings.Contains(refusal.Message, "ocel bootstrap") {
		t.Errorf("Put() refused with %q, want it to name the command that creates the bucket", refusal.Message)
	}
}

func TestLiveRemovingAPrefixLeavesEveryStoreItDoesNotName(t *testing.T) {
	provider := live(t)
	bucketsStanding(t, provider)

	ctx := context.Background()
	artifacts := provider.Artifacts()
	class := providerkit.ClassProduction
	swept, kept := "conformance/"+t.Name()+"/", "conformance/"+t.Name()+"-beside/"

	for _, store := range []string{providerkit.StoreFunctions, providerkit.StoreAssets, providerkit.StoreCache} {
		for _, prefix := range []string{swept, kept} {
			ref := providerkit.ArtifactRef{Class: class, Bucket: store, Key: prefix + "bundle.zip"}
			if err := artifacts.Put(ctx, ref, bytes.NewReader([]byte(store))); err != nil {
				t.Fatalf("Put(%s/%s) = %v", store, prefix, err)
			}
		}
	}

	if err := artifacts.RemovePrefix(ctx, class, swept, nil); err != nil {
		t.Fatalf("RemovePrefix(%s) = %v", swept, err)
	}

	for _, store := range []string{providerkit.StoreFunctions, providerkit.StoreAssets, providerkit.StoreCache} {
		gone, err := artifacts.Has(ctx, providerkit.ArtifactRef{Class: class, Bucket: store, Key: swept + "bundle.zip"})
		if err != nil || gone {
			t.Errorf("Has(%s/%s) = %v, %v, want it swept: one prefix names the same run in every store", store, swept, gone, err)
		}
		held, err := artifacts.Has(ctx, providerkit.ArtifactRef{Class: class, Bucket: store, Key: kept + "bundle.zip"})
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
	ref := providerkit.ArtifactRef{Class: providerkit.ClassProduction, Bucket: providerkit.StoreFunctions, Key: "conformance/" + t.Name() + "/bundle.zip"}
	if err := artifacts.Put(ctx, ref, bytes.NewReader([]byte("a build artifact"))); err != nil {
		t.Fatal(err)
	}

	var refusal providerkit.Refusal
	err := artifacts.RemovePrefix(ctx, providerkit.ClassProduction, "", nil)
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("RemovePrefix(\"\") = %v, want an %s refusal: an empty prefix names every artifact the class keeps", err, providerkit.CodeInvalid)
	}
	stored, err := artifacts.Has(ctx, ref)
	if err != nil || !stored {
		t.Fatalf("Has() after RemovePrefix(\"\") = %v, %v, want the artifact left where it was", stored, err)
	}
}

func TestLiveListingUnderANameReturnsTheRecordStoredAtIt(t *testing.T) {
	ctx := context.Background()
	records := live(t).Records()

	under := providerkit.RecordName{providerkit.RootConformance, string(providerkit.ClassProduction), t.Name()}
	for _, name := range []providerkit.RecordName{under, append(slices.Clone(under), "beneath")} {
		if _, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte(name.String())}); err != nil {
			t.Fatalf("Write(%s) = %v", name, err)
		}
	}

	held, err := records.List(ctx, under)
	if err != nil {
		t.Fatalf("List(%s) = %v", under, err)
	}
	if len(held) != 2 {
		t.Fatalf("List(%s) returned %d records, want the record at the name and the one beneath it: the sibling store answers with both", under, len(held))
	}
}
