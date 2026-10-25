package ports

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync/atomic"
	"testing"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

type droppingFirestore struct {
	firestorepb.UnimplementedFirestoreServer

	held          []records.Key
	refused       codes.Code
	dropped       atomic.Int32
	droppedMidway atomic.Int32
	asked         atomic.Int32
}

func (f *droppingFirestore) RunQuery(req *firestorepb.RunQueryRequest, stream firestorepb.Firestore_RunQueryServer) error {
	f.asked.Add(1)
	if f.refused != codes.OK {
		return status.Error(f.refused, "the caller may not list documents")
	}
	if f.dropped.Add(-1) >= 0 {
		return status.Error(codes.Unavailable, "read tcp 10.0.0.2:50122->142.250.0.95:443: read: connection timed out")
	}
	if len(f.held) == 0 {
		return stream.Send(&firestorepb.RunQueryResponse{ReadTime: timestamppb.Now()})
	}
	collection := req.GetParent() + "/" + req.GetStructuredQuery().GetFrom()[0].GetCollectionId() + "/"
	for sent, key := range f.held {
		if sent == 1 && f.droppedMidway.Add(-1) >= 0 {
			return status.Error(codes.Unavailable, "read tcp 10.0.0.2:50122->142.250.0.95:443: read: connection reset by peer")
		}
		document := &firestorepb.Document{
			Name: collection + documentID(key),
			Fields: map[string]*firestorepb.Value{
				valueField:    {ValueType: &firestorepb.Value_StringValue{StringValue: `"` + key.Path[0] + `"`}},
				revisionField: {ValueType: &firestorepb.Value_StringValue{StringValue: "1"}},
			},
			CreateTime: timestamppb.Now(),
			UpdateTime: timestamppb.Now(),
		}
		if err := stream.Send(&firestorepb.RunQueryResponse{Document: document, ReadTime: timestamppb.Now()}); err != nil {
			return err
		}
	}
	return nil
}

func (f *droppingFirestore) BatchGetDocuments(req *firestorepb.BatchGetDocumentsRequest, stream firestorepb.Firestore_BatchGetDocumentsServer) error {
	for _, name := range req.GetDocuments() {
		missing := &firestorepb.BatchGetDocumentsResponse{Result: &firestorepb.BatchGetDocumentsResponse_Missing{Missing: name}, ReadTime: timestamppb.Now()}
		if err := stream.Send(missing); err != nil {
			return err
		}
	}
	return nil
}

func droppingKeyValues(t *testing.T, fake *droppingFirestore) KeyValues {
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
	return KeyValues{Clients: &Clients{Namespace: "ocel", Project: "acme", Region: "us-central1", Endpoint: "http://" + listener.Addr().String()}}
}

func TestAnAbsentEntryWhoseTierCheckDropsIsCheckedAgainAndReadAsAbsent(t *testing.T) {
	fake := &droppingFirestore{}
	fake.dropped.Store(1)
	store := droppingKeyValues(t, fake)

	_, err := store.Read(context.Background(), records.Partition{Tier: environment.TierProduction, Root: "stacks"}.Key("shop"))

	if !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Read() = %v, want ErrNotFound once the dropped connection is reopened", err)
	}
	if got := fake.asked.Load(); got != 2 {
		t.Errorf("the tier was checked %d times, want 2: once on the dropped connection and once after", got)
	}
}

func TestAnAbsentEntryWhoseTierCheckKeepsDroppingIsAnErrorAndNotAnAbsentEntry(t *testing.T) {
	fake := &droppingFirestore{}
	fake.dropped.Store(googleretry.Attempts + 1)
	store := droppingKeyValues(t, fake)

	_, err := store.Read(context.Background(), records.Partition{Tier: environment.TierProduction, Root: "stacks"}.Key("shop"))

	if errors.Is(err, records.ErrNotFound) || status.Code(err) != codes.Unavailable {
		t.Errorf("Read() = %v, want the Unavailable answer, since a dropped connection says nothing of whether the entry exists", err)
	}
}

func TestAListingWhoseConnectionDropsIsReadAgain(t *testing.T) {
	fake := &droppingFirestore{}
	fake.dropped.Store(1)
	store := droppingKeyValues(t, fake)

	if _, err := store.List(context.Background(), records.Partition{Tier: environment.TierProduction, Root: "stacks", Path: []string{"shop"}}); err != nil {
		t.Fatalf("List() = %v, want the listing read again once the dropped connection is reopened", err)
	}
	if got := fake.asked.Load(); got != 2 {
		t.Errorf("the partition was queried %d times, want 2: once on the dropped connection and once after", got)
	}
}

func TestAnAbsentEntryWhoseTierCheckIsDeniedIsReadAsAbsent(t *testing.T) {
	fake := &droppingFirestore{refused: codes.PermissionDenied}
	store := droppingKeyValues(t, fake)

	_, err := store.Read(context.Background(), records.Partition{Tier: environment.TierProduction, Root: "stacks"}.Key("shop"))

	if !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Read() = %v, want ErrNotFound, since a credential that may get entries but not list them still reads an absent one", err)
	}
}

func TestAListingWhoseConnectionDropsMidwayIsReadAgainWhole(t *testing.T) {
	partition := records.Partition{Tier: environment.TierProduction, Root: "stacks", Path: []string{"shop"}}
	fake := &droppingFirestore{held: []records.Key{partition.Key("api"), partition.Key("web"), partition.Key("worker")}}
	fake.droppedMidway.Store(1)
	store := droppingKeyValues(t, fake)

	entries, err := store.List(context.Background(), partition)

	if err != nil {
		t.Fatalf("List() = %v, want the listing read again once the dropped connection is reopened", err)
	}
	var listed []string
	for _, entry := range entries {
		listed = append(listed, string(entry.Value))
	}
	if want := []string{`"api"`, `"web"`, `"worker"`}; !slices.Equal(listed, want) {
		t.Errorf("List() = %v, want %v", listed, want)
	}
	if got := fake.asked.Load(); got != 2 {
		t.Errorf("the partition was queried %d times, want 2: once on the dropped connection and once after", got)
	}
}
