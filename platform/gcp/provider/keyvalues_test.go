package gcp_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type answeringFirestore struct {
	firestorepb.UnimplementedFirestoreServer

	queries error
	holding string
}

func (a answeringFirestore) BatchGetDocuments(*firestorepb.BatchGetDocumentsRequest, firestorepb.Firestore_BatchGetDocumentsServer) error {
	return status.Error(codes.NotFound, `"projects/acme-prod/databases/ocel/documents/records-production/x" not found`)
}

func (a answeringFirestore) RunQuery(req *firestorepb.RunQueryRequest, stream firestorepb.Firestore_RunQueryServer) error {
	if a.queries != nil || a.holding == "" {
		return a.queries
	}
	collection := req.GetStructuredQuery().GetFrom()[0].GetCollectionId()
	written := timestamppb.Now()
	return stream.Send(&firestorepb.RunQueryResponse{
		ReadTime: written,
		Document: &firestorepb.Document{
			Name:       req.GetParent() + "/" + collection + "/" + a.holding,
			Fields:     map[string]*firestorepb.Value{"body": {ValueType: &firestorepb.Value_BytesValue{BytesValue: []byte("2")}}},
			CreateTime: written,
			UpdateTime: written,
		},
	})
}

func firestoreAnswering(t *testing.T, queries error) string {
	t.Helper()
	return firestoreServing(t, answeringFirestore{queries: queries})
}

func firestoreHolding(t *testing.T, id string) string {
	t.Helper()
	return firestoreServing(t, answeringFirestore{holding: id})
}

func firestoreServing(t *testing.T, answering answeringFirestore) string {
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

func TestReadingWhereTheDatabaseIsAbsentSaysWhatToRunRatherThanThatTheEntryIsMissing(t *testing.T) {
	name := keyvalue.Partition{Tier: environment.TierProduction, Root: keyvalue.RootConformance}.Key("absent")

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

		if _, err := testProvider(t).KeyValues().Read(context.Background(), name); !errors.Is(err, keyvalue.ErrNotFound) {
			t.Fatalf("Read() of a name nothing was written at = %v, want ErrNotFound", err)
		}
	})
}

func TestASchemaTheOlderLayoutWroteIsRefusedRatherThanStampedOver(t *testing.T) {
	t.Run("the older layout's schema document", func(t *testing.T) {
		t.Setenv("OCEL_FLOCI_GCP_ENDPOINT", firestoreHolding(t, "schema#production"))

		var refused refusal.Refusal
		err := stackrecords.EnsureSchema(context.Background(), testProvider(t).KeyValues(), environment.TierProduction)
		if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
			t.Fatalf("EnsureSchema() over a schema the older layout wrote = %v, want a %s refusal: a build that reads it as unwritten stamps its own schema beside records it cannot see", err, refusal.CodeNotReady)
		}
		if !strings.Contains(refused.Message, "older ocel") {
			t.Errorf("EnsureSchema() refused with %q, want it to say an older ocel wrote what is there", refused.Message)
		}
	})

	t.Run("a document the older layout wrote for another key", func(t *testing.T) {
		t.Setenv("OCEL_FLOCI_GCP_ENDPOINT", firestoreHolding(t, "stacks#production#shop"))

		key := stackrecords.SchemaKey(environment.TierProduction)
		if _, err := testProvider(t).KeyValues().Read(context.Background(), key); !errors.Is(err, keyvalue.ErrNotFound) {
			t.Fatalf("Read(%s) beside an older document for another key = %v, want ErrNotFound", key, err)
		}
	})
}
