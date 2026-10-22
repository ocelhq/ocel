package gcp_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type answeringFirestore struct {
	firestorepb.UnimplementedFirestoreServer

	queries error
	present string
}

func (a answeringFirestore) BatchGetDocuments(*firestorepb.BatchGetDocumentsRequest, firestorepb.Firestore_BatchGetDocumentsServer) error {
	return status.Error(codes.NotFound, `"projects/acme-prod/databases/ocel/documents/keyvalues-production/x" not found`)
}

func (a answeringFirestore) RunQuery(req *firestorepb.RunQueryRequest, _ firestorepb.Firestore_RunQueryServer) error {
	if a.present != "" && strings.Contains(req.GetParent(), "/databases/"+a.present+"/") {
		return nil
	}
	return a.queries
}

func firestoreAnswering(t *testing.T, queries error) string {
	t.Helper()
	return serveFirestore(t, answeringFirestore{queries: queries})
}

func firestoreHoldingOnly(t *testing.T, tier environment.Tier) string {
	t.Helper()
	return serveFirestore(t, answeringFirestore{
		queries: status.Error(codes.NotFound, "The database does not exist for project acme-prod"),
		present: ports.RecordsDatabase(provider.DefaultNamespace, tier),
	})
}

func serveFirestore(t *testing.T, answering answeringFirestore) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	server := grpc.NewServer()
	firestorepb.RegisterFirestoreServer(server, answering)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return "http://" + listener.Addr().String()
}

func cloudflareAccountHoldingNothing(t *testing.T) {
	t.Helper()
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct")
	t.Setenv("CLOUDFLARE_API_TOKEN", "tok")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := `[]`
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/zones"):
			if page := r.URL.Query().Get("page"); page == "" || page == "1" {
				result = `[{"id":"zone-example","name":"example.com"}]`
			}
		case r.Method == http.MethodGet && (strings.HasSuffix(r.URL.Path, "/dns_records") || strings.HasSuffix(r.URL.Path, "/workers/routes")):
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/dns_records"):
			result = `{"id":"rec-shop","type":"A","name":"shop.example.com","content":"203.0.113.7"}`
		default:
			http.Error(w, `{"success":false,"errors":[{"code":10007,"message":"not found"}]}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":` + result + `,"result_info":{"page":1,"per_page":20,"count":1,"total_count":1,"total_pages":1}}`))
	}))
	t.Cleanup(api.Close)
	t.Setenv("CLOUDFLARE_BASE_URL", api.URL+"/")
}

func TestAFirstBootstrapBehindCloudflarePlansBeforeTheTiersRecordsDatabaseExists(t *testing.T) {
	t.Setenv("OCEL_FLOCI_GCP_ENDPOINT",
		firestoreAnswering(t, status.Error(codes.NotFound, "The database (projects/acme-prod/databases/ocel) does not exist for project acme-prod")))
	cloudflareAccountHoldingNothing(t)
	front, err := testProvider(t).Edges().Open(cloudflare.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}

	planned, err := front.Hooks().PlanBootstrap(t.Context(), environment.TierProduction)
	if err != nil {
		t.Fatalf("PlanBootstrap() before the tier's records database exists = %v, want the plan drawn: the bootstrap it plans is what creates that database", err)
	}
	if !slices.ContainsFunc(planned, func(change edge.PlanChange) bool { return change.Action == edge.PlanCreate }) {
		t.Errorf("PlanBootstrap() = %+v, want the workers it creates", planned)
	}
}

func TestAProjectBootstrappedOnlyForPreviewWritesDNSAndReadsWhoOwnsAHostnameThroughCloudflare(t *testing.T) {
	t.Setenv("OCEL_FLOCI_GCP_ENDPOINT", firestoreHoldingOnly(t, environment.TierPreview))
	cloudflareAccountHoldingNothing(t)
	p := testProvider(t)
	ctx := t.Context()

	records, err := p.DNS().Open(provider.DNSKind(cloudflare.Kind), "", cloudflare.Kind)
	if err != nil {
		t.Fatal(err)
	}
	wanted := edge.Record{Name: "shop.example.com", Type: edge.RecordTypeA, Value: "203.0.113.7"}
	if written, err := records.Ensure(ctx, []edge.Record{wanted}, nil); err != nil || len(written) != 1 {
		t.Errorf("DNS Ensure() = %v, %v, want the record written: production has no records database to hold a Cloudflare account, and preview's is the one to compare", written, err)
	}
	front, err := p.Edges().Open(cloudflare.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}
	if owner, err := front.DomainOwner(ctx, wanted.Name); err != nil || owner != "" {
		t.Errorf("DomainOwner() = %q, %v, want no owner read without a refusal", owner, err)
	}
}

func TestReadingWhereTheDatabaseIsAbsentSaysWhatToRunRatherThanThatTheEntryIsMissing(t *testing.T) {
	name := records.Partition{Tier: environment.TierProduction, Root: conformance.Root}.Key("absent")

	t.Run("a database that is not there", func(t *testing.T) {
		t.Setenv("OCEL_FLOCI_GCP_ENDPOINT",
			firestoreAnswering(t, status.Error(codes.NotFound, "The database (projects/acme-prod/databases/ocel) does not exist for project acme-prod")))

		var refused refusal.Refusal
		_, err := testProvider(t).KeyValues().Read(context.Background(), name)
		if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
			t.Fatalf("Read() where no database exists = %v, want a %s refusal: a caller that reads ErrNotFound writes on, and the write has nowhere to land", err, refusal.CodeNotReady)
		}
		if !strings.Contains(refused.Message, "ocel bootstrap") {
			t.Errorf("Read() refused with %q, want it to name the command that creates the database", refused.Message)
		}
	})

	t.Run("a database that is there and has no such document", func(t *testing.T) {
		t.Setenv("OCEL_FLOCI_GCP_ENDPOINT", firestoreAnswering(t, nil))

		if _, err := testProvider(t).KeyValues().Read(context.Background(), name); !errors.Is(err, records.ErrNotFound) {
			t.Fatalf("Read() of a name nothing was written at = %v, want ErrNotFound", err)
		}
	})
}
