package gcp_test

import (
	"bytes"
	"context"
	"os"
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
