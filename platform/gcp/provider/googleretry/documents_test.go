package googleretry_test

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync/atomic"
	"testing"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const queriedCollection = "projects/acme/databases/(default)/documents/entries/"

type droppingFirestore struct {
	firestorepb.UnimplementedFirestoreServer

	held    []string
	dropped atomic.Int32
	asked   atomic.Int32
}

func (f *droppingFirestore) RunQuery(_ *firestorepb.RunQueryRequest, stream firestorepb.Firestore_RunQueryServer) error {
	f.asked.Add(1)
	if f.dropped.Add(-1) >= 0 {
		return status.Error(codes.Unavailable, "read tcp 10.0.0.2:50122->142.250.0.95:443: read: connection timed out")
	}
	for _, id := range f.held {
		document := &firestorepb.Document{Name: queriedCollection + id, CreateTime: timestamppb.Now(), UpdateTime: timestamppb.Now()}
		if err := stream.Send(&firestorepb.RunQueryResponse{Document: document, ReadTime: timestamppb.Now()}); err != nil {
			return err
		}
	}
	return nil
}

func servedBy(t *testing.T, fake *droppingFirestore) *firestore.Client {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	firestorepb.RegisterFirestoreServer(server, fake)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	client, err := firestore.NewClient(context.Background(), "acme",
		option.WithEndpoint(listener.Addr().String()),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestAQueryWhoseFirstAnswerDropsIsRunAgainAndYieldsEveryDocument(t *testing.T) {
	t.Parallel()
	fake := &droppingFirestore{held: []string{"a", "b", "c"}}
	fake.dropped.Store(2)
	client := servedBy(t, fake)

	documents := googleretry.QueryDocuments(context.Background(), client.Collection("entries").Query)
	defer documents.Stop()
	var read []string
	for {
		document, err := documents.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			t.Fatalf("Next() = %v, want the documents once the dropped connection is reopened", err)
		}
		read = append(read, document.Ref.ID)
	}

	if !slices.Equal(read, fake.held) {
		t.Errorf("read %v, want %v", read, fake.held)
	}
	if got := fake.asked.Load(); got != 3 {
		t.Errorf("the query ran %d times, want 3: twice on dropped connections and once after", got)
	}
}

func TestAQueryWhoseConnectionKeepsDroppingAnswersTheDropAfterItsAttempts(t *testing.T) {
	t.Parallel()
	fake := &droppingFirestore{}
	fake.dropped.Store(googleretry.Attempts + 1)
	client := servedBy(t, fake)

	documents := googleretry.QueryDocuments(context.Background(), client.Collection("entries").Query)
	defer documents.Stop()
	_, err := documents.Next()

	if status.Code(err) != codes.Unavailable {
		t.Errorf("Next() = %v, want the Unavailable answer of the last attempt", err)
	}
	if _, again := documents.Next(); status.Code(again) != codes.Unavailable {
		t.Errorf("Next() after the drop = %v, want the same Unavailable answer", again)
	}
	if got := fake.asked.Load(); got != googleretry.Attempts {
		t.Errorf("the query ran %d times, want %d", got, googleretry.Attempts)
	}
}
