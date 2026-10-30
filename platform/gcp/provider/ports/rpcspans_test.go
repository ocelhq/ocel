package ports_test

import (
	"context"
	"net"
	"slices"
	"testing"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/firestore/apiv1/firestorepb"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func onlySpanNamed(t *testing.T, spans *tracingtest.Spans, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	var named []sdktrace.ReadOnlySpan
	for _, span := range spans.Ended() {
		if span.Name() == name {
			named = append(named, span)
		}
	}
	if len(named) != 1 {
		t.Fatalf("made %d spans named %s, want 1", len(named), name)
	}
	return named[0]
}

func stringSliceAttribute(span sdktrace.ReadOnlySpan, key string) []string {
	value := tracingtest.Attributes(span)[key]
	return value.AsStringSlice()
}

type missingDocuments struct {
	firestorepb.UnimplementedFirestoreServer
}

func (missingDocuments) BatchGetDocuments(asked *firestorepb.BatchGetDocumentsRequest, stream firestorepb.Firestore_BatchGetDocumentsServer) error {
	for _, name := range asked.GetDocuments() {
		if err := stream.Send(&firestorepb.BatchGetDocumentsResponse{
			Result:   &firestorepb.BatchGetDocumentsResponse_Missing{Missing: name},
			ReadTime: timestamppb.Now(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func serveFirestore(t *testing.T, server firestorepb.FirestoreServer) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := grpc.NewServer()
	firestorepb.RegisterFirestoreServer(served, server)
	go func() { _ = served.Serve(listener) }()
	t.Cleanup(served.Stop)
	return "http://" + listener.Addr().String()
}

func TestAFirestoreReadIsOneSpanNamingTheDocumentItReads(t *testing.T) {
	ctx, spans := tracingtest.Record(t)
	endpoint := serveFirestore(t, missingDocuments{})
	client, err := firestore.NewClientWithDatabase(context.Background(), "acme", "records", ports.NewGRPCOptions(ports.Credentials{}, endpoint, "")...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if _, err := client.Collection("stacks").Doc("web").Get(ctx); err == nil {
		t.Fatal("Get of a missing document succeeded")
	}

	read := onlySpanNamed(t, spans, "google.firestore.v1.Firestore/BatchGetDocuments")
	want := "projects/acme/databases/records/documents/stacks/web"
	if names := stringSliceAttribute(read, "gcp.resource.destination.id"); !slices.Contains(names, want) {
		t.Errorf("the read's span names resources %q, want it to name %s", names, want)
	}
}

type acceptedWrites struct {
	firestorepb.UnimplementedFirestoreServer
}

func (acceptedWrites) Commit(_ context.Context, asked *firestorepb.CommitRequest) (*firestorepb.CommitResponse, error) {
	results := make([]*firestorepb.WriteResult, len(asked.GetWrites()))
	for i := range results {
		results[i] = &firestorepb.WriteResult{UpdateTime: timestamppb.Now()}
	}
	return &firestorepb.CommitResponse{WriteResults: results, CommitTime: timestamppb.Now()}, nil
}

func TestAFirestoreWriteSpanNamesTheDocumentButNotTheValuesWritten(t *testing.T) {
	ctx, spans := tracingtest.Record(t)
	endpoint := serveFirestore(t, acceptedWrites{})
	client, err := firestore.NewClientWithDatabase(context.Background(), "acme", "records", ports.NewGRPCOptions(ports.Credentials{}, endpoint, "")...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	written := "projects/acme/secrets/session-key"
	if _, err := client.Collection("values").Doc("web").Set(ctx, map[string]any{"value": written}); err != nil {
		t.Fatal(err)
	}

	commit := onlySpanNamed(t, spans, "google.firestore.v1.Firestore/Commit")
	names := stringSliceAttribute(commit, "gcp.resource.destination.id")
	if want := "projects/acme/databases/records/documents/values/web"; !slices.Contains(names, want) {
		t.Errorf("the write's span names resources %q, want it to name %s", names, want)
	}
	if slices.Contains(names, written) {
		t.Errorf("the write's span names resources %q, which carry a value the document holds", names)
	}
}
