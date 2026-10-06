package gcp

import (
	"context"
	"maps"
	"net"
	"slices"
	"sync"
	"testing"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
)

type taggedFirestore struct {
	firestorepb.UnimplementedFirestoreServer

	mu      sync.Mutex
	listed  []string
	queries []*firestorepb.StructuredQuery
	deleted []string
	absent  bool
}

func (f *taggedFirestore) RunQuery(request *firestorepb.RunQueryRequest, stream firestorepb.Firestore_RunQueryServer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, request.GetStructuredQuery())
	if f.absent {
		return status.Error(codes.NotFound, "The database (projects/acme-prod/databases/ocel-production-tags) does not exist for project acme-prod")
	}
	for _, name := range f.listed {
		if err := stream.Send(&firestorepb.RunQueryResponse{
			Document: &firestorepb.Document{Name: name, CreateTime: timestamppb.Now(), UpdateTime: timestamppb.Now()},
			ReadTime: timestamppb.Now(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (f *taggedFirestore) BatchWrite(_ context.Context, request *firestorepb.BatchWriteRequest) (*firestorepb.BatchWriteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	response := &firestorepb.BatchWriteResponse{}
	for _, write := range request.GetWrites() {
		f.deleted = append(f.deleted, write.GetDelete())
		response.Status = append(response.Status, status.New(codes.OK, "").Proto())
		response.WriteResults = append(response.WriteResults, &firestorepb.WriteResult{UpdateTime: timestamppb.Now()})
	}
	return response, nil
}

func tagRecordsServedBy(t *testing.T, fake *taggedFirestore) *Provider {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	server := grpc.NewServer()
	firestorepb.RegisterFirestoreServer(server, fake)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return pushing(t, "http://"+listener.Addr().String())
}

func tagDocument(id string) string {
	return "projects/acme-prod/databases/ocel-production-tags/documents/tags/" + id
}

type sayings struct {
	progress.Log
	said []string
}

func (s *sayings) Say(message string) { s.said = append(s.said, message) }

func prefixBounds(t *testing.T, query *firestorepb.StructuredQuery) map[string]string {
	t.Helper()
	bounds := map[string]string{}
	for _, filter := range query.GetWhere().GetCompositeFilter().GetFilters() {
		field := filter.GetFieldFilter()
		if field.GetField().GetFieldPath() != "prefix" {
			t.Fatalf("the prune filters on %q, want only prefix", field.GetField().GetFieldPath())
		}
		bounds[field.GetOp().String()] = field.GetValue().GetStringValue()
	}
	return bounds
}

func TestPruningAReleaseDeletesTheTagRecordsOfItsISRPrefixAndNoOther(t *testing.T) {
	fake := &taggedFirestore{listed: []string{tagDocument("aaa"), tagDocument("bbb")}}
	p := tagRecordsServedBy(t, fake)
	log := &sayings{Log: progress.Discard()}

	if err := (artifacts{p}).removeTagRecords(context.Background(), environment.TierProduction, "prod/shop/web/r1a2b3c4d/isr/", log); err != nil {
		t.Fatalf("removeTagRecords() = %v", err)
	}

	if len(fake.queries) != 1 {
		t.Fatalf("the prune ran %d queries, want one", len(fake.queries))
	}
	want := map[string]string{"GREATER_THAN_OR_EQUAL": "prod/shop/web/r1a2b3c4d/isr/", "LESS_THAN": "prod/shop/web/r1a2b3c4d/isr/"}
	if got := prefixBounds(t, fake.queries[0]); !maps.Equal(got, want) {
		t.Errorf("the prune ranged over %q, want %q", got, want)
	}
	if got := fake.queries[0].GetFrom()[0].GetCollectionId(); got != "tags" {
		t.Errorf("the prune read collection %q, want tags", got)
	}
	slices.Sort(fake.deleted)
	if want := []string{tagDocument("aaa"), tagDocument("bbb")}; !slices.Equal(fake.deleted, want) {
		t.Errorf("the prune deleted %q, want %q", fake.deleted, want)
	}
	if want := []string{"Removed 2 tag records under prod/shop/web/r1a2b3c4d/isr/ from Firestore database ocel-production-tags"}; !slices.Equal(log.said, want) {
		t.Errorf("the prune said %q, want %q", log.said, want)
	}
}

func TestPruningAReleaseByItsWholePrefixRangesOverTheISRPrefixToo(t *testing.T) {
	fake := &taggedFirestore{}
	p := tagRecordsServedBy(t, fake)

	if err := (artifacts{p}).removeTagRecords(context.Background(), environment.TierProduction, "prod/shop/web/r1a2b3c4d/", nil); err != nil {
		t.Fatalf("removeTagRecords() = %v", err)
	}

	want := map[string]string{"GREATER_THAN_OR_EQUAL": "prod/shop/web/r1a2b3c4d/", "LESS_THAN": "prod/shop/web/r1a2b3c4d/"}
	if got := prefixBounds(t, fake.queries[0]); !maps.Equal(got, want) {
		t.Errorf("the prune ranged over %q, want %q: the isr prefix lies inside it", got, want)
	}
}

func TestRemovingAProjectsEnvironmentDeletesOnlyThatEnvironmentsTagRecords(t *testing.T) {
	fake := &taggedFirestore{listed: []string{tagDocument("aaa")}}
	p := tagRecordsServedBy(t, fake)

	if err := (artifacts{p}).removeTagRecords(context.Background(), environment.TierPreview, "pr-7/shop/", nil); err != nil {
		t.Fatalf("removeTagRecords() = %v", err)
	}

	want := map[string]string{"GREATER_THAN_OR_EQUAL": "pr-7/shop/", "LESS_THAN": "pr-7/shop/"}
	if got := prefixBounds(t, fake.queries[0]); !maps.Equal(got, want) {
		t.Errorf("the prune ranged over %q, want %q: pr-70/shop/ and prod/shop/ fall outside it", got, want)
	}
}

func TestPruningATierWithNoTagsDatabaseSucceeds(t *testing.T) {
	fake := &taggedFirestore{absent: true}
	p := tagRecordsServedBy(t, fake)
	log := &sayings{Log: progress.Discard()}

	if err := (artifacts{p}).removeTagRecords(context.Background(), environment.TierProduction, "prod/shop/web/r1/isr/", log); err != nil {
		t.Fatalf("removeTagRecords() with no tags database = %v, want nil", err)
	}
	if len(log.said) != 0 {
		t.Errorf("the prune said %q, want nothing when nothing was removed", log.said)
	}
}
