package ports

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func databaseOf(t *testing.T, store KeyValues, in records.Partition) string {
	t.Helper()
	_, collection, err := store.open(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	_, rest, _ := strings.Cut(collection.Path, "/databases/")
	database, _, _ := strings.Cut(rest, "/")
	return database
}

func TestAProjectWithNoDatabaseOnRecordKeepsItsRecordsInTheDatabaseItsTierShares(t *testing.T) {
	t.Parallel()

	database, err := ReadRecordedDatabase(context.Background(), fake.NewKeyValues(), "ocel", environment.TierPreview, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if want := SharedDatabase("ocel", environment.TierPreview); database != want {
		t.Errorf("shop keeps its records in %q, want the preview tier's shared %q", database, want)
	}
}

func TestAProjectRecordedWithADatabaseOfItsOwnKeepsItsRecordsThere(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	own := OwnDatabase("ocel", environment.TierPreview, "shop")
	value, err := ProjectDatabaseRecord(own)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(ctx, records.Entry{Key: ProjectDatabaseKey(environment.TierPreview, "shop"), Value: value}); err != nil {
		t.Fatal(err)
	}

	database, err := ReadRecordedDatabase(ctx, store, "ocel", environment.TierPreview, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if database != own {
		t.Errorf("shop keeps its records in %q, want its own %q", database, own)
	}
	blog, err := ReadRecordedDatabase(ctx, store, "ocel", environment.TierPreview, "blog")
	if err != nil {
		t.Fatal(err)
	}
	if blog != SharedDatabase("ocel", environment.TierPreview) {
		t.Errorf("blog keeps its records in %q, want the shared database: only shop has a database of its own", blog)
	}
}

func TestAProjectsValuesAreKeptInTheDatabaseTheRuntimeIsToldAndATiersRecordsInItsOwn(t *testing.T) {
	isolated(t)
	own := OwnDatabase("ocel", environment.TierPreview, "shop")
	store := KeyValues{Clients: &Clients{
		Namespace: "ocel", Project: "acme", Region: "us-central1", Endpoint: "http://127.0.0.1:1",
		ProjectDatabases: map[string]string{"shop": own, "blog": SharedDatabase("ocel", environment.TierPreview)},
	}}
	shop := variablestore.ValuesPartition(variablestore.Scope{Project: "shop", Tier: environment.TierPreview})
	blog := variablestore.ValuesPartition(variablestore.Scope{Project: "blog", Tier: environment.TierPreview})
	stacks := records.Partition{Tier: environment.TierPreview, Root: stackrecords.RootStacks, Path: []string{"shop"}}

	if got := databaseOf(t, store, shop); got != own {
		t.Errorf("shop's values are kept in %q, want its own %q", got, own)
	}
	if got, want := databaseOf(t, store, blog), SharedDatabase("ocel", environment.TierPreview); got != want {
		t.Errorf("blog's values are kept in %q, want the shared %q", got, want)
	}
	if got, want := databaseOf(t, store, stacks), RecordsDatabase("ocel", environment.TierPreview); got != want {
		t.Errorf("shop's stack records are kept in %q, want the preview tier's own %q", got, want)
	}
}

func TestEveryAppDatabaseIsNamedUnderItsTiersFenceAndFitsWhatFirestoreTakes(t *testing.T) {
	t.Parallel()
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		own := OwnDatabase(provider19, tier, "a-project-slug-that-runs-long")
		if !strings.HasPrefix(own, OwnDatabasePrefix(provider19, tier)) || len(own) > 63 {
			t.Errorf("%s database %q, want it under %q and at most 63 characters", tier, own, OwnDatabasePrefix(provider19, tier))
		}
		if shared := SharedDatabase(provider19, tier); len(shared) > 63 || strings.HasPrefix(shared, OwnDatabasePrefix(provider19, tier)) {
			t.Errorf("%s shared database %q, want at most 63 characters and outside the prefix of databases a project owns", tier, shared)
		}
	}
	names := []string{
		OwnDatabasePrefix("ocel", environment.TierProduction), OwnDatabasePrefix("ocel", environment.TierPreview),
		SharedDatabase("ocel", environment.TierProduction), SharedDatabase("ocel", environment.TierPreview),
	}
	for i, name := range names {
		for j, other := range names {
			if i != j && strings.HasPrefix(other, name) {
				t.Errorf("%q also names %q, so a condition on it reaches that database", name, other)
			}
		}
	}
}

const provider19 = "nineteen-characters"

type freshFirestore struct {
	firestorepb.UnimplementedFirestoreServer

	unserved   atomic.Int32
	asked      atomic.Int32
	unwritable atomic.Int32
	begun      atomic.Int32
}

func (f *freshFirestore) BeginTransaction(context.Context, *firestorepb.BeginTransactionRequest) (*firestorepb.BeginTransactionResponse, error) {
	f.begun.Add(1)
	if f.unwritable.Add(-1) >= 0 {
		return nil, status.Error(codes.NotFound, "The database does not exist for project acme")
	}
	return &firestorepb.BeginTransactionResponse{Transaction: []byte("t1")}, nil
}

func (f *freshFirestore) Commit(context.Context, *firestorepb.CommitRequest) (*firestorepb.CommitResponse, error) {
	return &firestorepb.CommitResponse{CommitTime: timestamppb.Now()}, nil
}

func (f *freshFirestore) Rollback(context.Context, *firestorepb.RollbackRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func servedFresh(t *testing.T, fake *freshFirestore) *Clients {
	t.Helper()
	isolated(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	firestorepb.RegisterFirestoreServer(server, fake)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return &Clients{Namespace: "ocel", Project: "acme", Region: "us-central1", Endpoint: "http://" + listener.Addr().String()}
}

func (f *freshFirestore) RunQuery(_ *firestorepb.RunQueryRequest, stream firestorepb.Firestore_RunQueryServer) error {
	f.asked.Add(1)
	if f.unserved.Add(-1) >= 0 {
		return status.Error(codes.NotFound, "The database does not exist for project acme")
	}
	return stream.Send(&firestorepb.RunQueryResponse{ReadTime: timestamppb.Now()})
}

func TestADatabaseFirestoreDoesNotServeYetIsWaitedForUntilItAnswersAQuery(t *testing.T) {
	fake := &freshFirestore{}
	fake.unserved.Store(2)
	clients := servedFresh(t, fake)

	if err := UntilDatabaseServes(context.Background(), clients, SharedDatabase("ocel", environment.TierProduction)); err != nil {
		t.Fatalf("UntilDatabaseServes() = %v, want it to return once the database answers", err)
	}
	if got := fake.asked.Load(); got != 3 {
		t.Errorf("the database was queried %d times, want 3: twice before it served and once after", got)
	}
}

func TestADatabaseThatAnswersQueriesButTakesNoWriteYetIsWaitedFor(t *testing.T) {
	fake := &freshFirestore{}
	fake.unwritable.Store(2)
	clients := servedFresh(t, fake)

	if err := UntilDatabaseServes(context.Background(), clients, SharedDatabase("ocel", environment.TierProduction)); err != nil {
		t.Fatalf("UntilDatabaseServes() = %v, want it to return once the database takes a write", err)
	}
	if got := fake.begun.Load(); got != 3 {
		t.Errorf("a write was begun %d times, want 3: the bootstrap's first record is a write, and Firestore refused the first two as for a database that does not exist", got)
	}
}
