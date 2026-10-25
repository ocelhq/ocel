package ports

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
)

type droppingFirestore struct {
	firestorepb.UnimplementedFirestoreServer

	dropped atomic.Int32
	asked   atomic.Int32
}

func (f *droppingFirestore) RunQuery(_ *firestorepb.RunQueryRequest, stream firestorepb.Firestore_RunQueryServer) error {
	f.asked.Add(1)
	if f.dropped.Add(-1) >= 0 {
		return status.Error(codes.Unavailable, "read tcp 10.0.0.2:50122->142.250.0.95:443: read: connection timed out")
	}
	return stream.Send(&firestorepb.RunQueryResponse{ReadTime: timestamppb.Now()})
}

func TestAListingWhoseConnectionDropsIsReadAgain(t *testing.T) {
	isolated(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := &droppingFirestore{}
	fake.dropped.Store(1)
	server := grpc.NewServer()
	firestorepb.RegisterFirestoreServer(server, fake)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	store := KeyValues{Clients: &Clients{Namespace: "ocel", Project: "acme", Region: "us-central1", Endpoint: "http://" + listener.Addr().String()}}

	if _, err := store.List(context.Background(), records.Partition{Tier: environment.TierProduction, Root: "stacks", Path: []string{"shop"}}); err != nil {
		t.Fatalf("List() = %v, want the listing read again once the dropped connection is reopened", err)
	}
	if got := fake.asked.Load(); got != 2 {
		t.Errorf("the partition was queried %d times, want 2: once on the dropped connection and once after", got)
	}
}
