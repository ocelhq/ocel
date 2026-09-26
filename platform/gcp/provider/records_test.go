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

	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type answeringFirestore struct {
	firestorepb.UnimplementedFirestoreServer

	queries error
}

func (a answeringFirestore) BatchGetDocuments(*firestorepb.BatchGetDocumentsRequest, firestorepb.Firestore_BatchGetDocumentsServer) error {
	return status.Error(codes.NotFound, `"projects/acme-prod/databases/ocel/documents/records-production/x" not found`)
}

func (a answeringFirestore) RunQuery(*firestorepb.RunQueryRequest, firestorepb.Firestore_RunQueryServer) error {
	return a.queries
}

func firestoreAnswering(t *testing.T, queries error) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	server := grpc.NewServer()
	firestorepb.RegisterFirestoreServer(server, answeringFirestore{queries: queries})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return "http://" + listener.Addr().String()
}

func TestReadingWhereTheDatabaseIsAbsentSaysWhatToRunRatherThanThatTheRecordIsMissing(t *testing.T) {
	name := records.Name{records.RootConformance, string(edge.ClassProduction), "absent"}

	t.Run("a database that is not there", func(t *testing.T) {
		t.Setenv("OCEL_FLOCI_GCP_ENDPOINT",
			firestoreAnswering(t, status.Error(codes.NotFound, "The database (projects/acme-prod/databases/ocel) does not exist for project acme-prod")))

		var refused refusal.Refusal
		_, err := testProvider(t).Records().Read(context.Background(), name)
		if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
			t.Fatalf("Read() where no database exists = %v, want a %s refusal: a caller that reads ErrNotFound writes on, and the write has nowhere to land", err, refusal.CodeNotReady)
		}
		if !strings.Contains(refused.Message, "ocel bootstrap") {
			t.Errorf("Read() refused with %q, want it to name the command that creates the database", refused.Message)
		}
	})

	t.Run("a database that is there and has no such document", func(t *testing.T) {
		t.Setenv("OCEL_FLOCI_GCP_ENDPOINT", firestoreAnswering(t, nil))

		if _, err := testProvider(t).Records().Read(context.Background(), name); !errors.Is(err, records.ErrNotFound) {
			t.Fatalf("Read() of a name nothing was written at = %v, want ErrNotFound", err)
		}
	})
}
