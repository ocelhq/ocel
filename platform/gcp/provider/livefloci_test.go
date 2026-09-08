package gcp_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
	return gcp.NewProvider(gcp.Options{Project: liveProject, Region: liveRegion})
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

func keysStanding(t *testing.T, region string) {
	t.Helper()

	ctx := context.Background()
	client, err := kms.NewKeyManagementClient(ctx, gcp.EmulatorGRPC(os.Getenv("OCEL_FLOCI_GCP_ENDPOINT"))...)
	if err != nil {
		t.Fatalf("reach the emulator's key manager: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	parent := "projects/" + liveProject + "/locations/" + region
	if _, err := client.CreateKeyRing(ctx, &kmspb.CreateKeyRingRequest{
		Parent: parent, KeyRingId: gcp.KeyRing, KeyRing: &kmspb.KeyRing{},
	}); err != nil && status.Code(err) != codes.AlreadyExists {
		t.Fatalf("create the %s key ring: %v", gcp.KeyRing, err)
	}
	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		if _, err := client.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
			Parent:      parent + "/keyRings/" + gcp.KeyRing,
			CryptoKeyId: string(class),
			CryptoKey:   &kmspb.CryptoKey{Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT},
		}); err != nil && status.Code(err) != codes.AlreadyExists {
			t.Fatalf("create the %s key: %v", class, err)
		}
	}
}

func TestLiveSealer(t *testing.T) {
	provider := live(t)
	keysStanding(t, liveRegion)

	conformance.RunSealer(t, provider.Sealer())
}

func TestLiveSealingWhereNoKeyRingStandsSaysWhatToRun(t *testing.T) {
	live(t)

	elsewhere := gcp.NewProvider(gcp.Options{Project: liveProject, Region: "australia-southeast2"})
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
