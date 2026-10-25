package topics_test

import (
	"context"
	"maps"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

type heldFirestore struct {
	firestorepb.UnimplementedFirestoreServer

	mu      sync.Mutex
	held    map[string]bool
	listed  []string
	queried []string
	dropped atomic.Int32
}

func (f *heldFirestore) ListDocuments(_ context.Context, req *firestorepb.ListDocumentsRequest) (*firestorepb.ListDocumentsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	collection := req.GetParent() + "/" + req.GetCollectionId() + "/"
	f.listed = append(f.listed, collection)
	seen := map[string]bool{}
	response := &firestorepb.ListDocumentsResponse{}
	for name := range f.held {
		rest, under := strings.CutPrefix(name, collection)
		if !under {
			continue
		}
		id, _, _ := strings.Cut(rest, "/")
		if !seen[id] {
			seen[id] = true
			response.Documents = append(response.Documents, &firestorepb.Document{Name: collection + id})
		}
	}
	return response, nil
}

func (f *heldFirestore) RunQuery(req *firestorepb.RunQueryRequest, stream firestorepb.Firestore_RunQueryServer) error {
	if f.dropped.Add(-1) >= 0 {
		return status.Error(codes.Unavailable, "read tcp 10.0.0.2:50122->142.250.0.95:443: read: connection timed out")
	}
	f.mu.Lock()
	collection := req.GetParent() + "/" + req.GetStructuredQuery().GetFrom()[0].GetCollectionId() + "/"
	f.queried = append(f.queried, collection)
	var found []string
	for name := range f.held {
		if rest, under := strings.CutPrefix(name, collection); under && !strings.Contains(rest, "/") {
			found = append(found, name)
		}
	}
	f.mu.Unlock()
	for _, name := range found {
		document := &firestorepb.Document{Name: name, CreateTime: timestamppb.Now(), UpdateTime: timestamppb.Now()}
		if err := stream.Send(&firestorepb.RunQueryResponse{Document: document, ReadTime: timestamppb.Now()}); err != nil {
			return err
		}
	}
	return stream.Send(&firestorepb.RunQueryResponse{ReadTime: timestamppb.Now()})
}

func (f *heldFirestore) BatchWrite(_ context.Context, req *firestorepb.BatchWriteRequest) (*firestorepb.BatchWriteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	response := &firestorepb.BatchWriteResponse{}
	for _, write := range req.GetWrites() {
		delete(f.held, write.GetDelete())
		response.WriteResults = append(response.WriteResults, &firestorepb.WriteResult{UpdateTime: timestamppb.Now()})
		response.Status = append(response.Status, status.New(codes.OK, "").Proto())
	}
	return response, nil
}

func holding(t *testing.T, fake *heldFirestore) *ports.Clients {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	server := grpc.NewServer()
	firestorepb.RegisterFirestoreServer(server, fake)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return &ports.Clients{Namespace: "ocel", Project: "acme-prod", Region: "europe-west1", Endpoint: "http://" + listener.Addr().String()}
}

func TestRemovingAProjectsRunsWhoseListingDropsListsThemAgainAndDeletesThem(t *testing.T) {
	t.Parallel()
	tier := environment.TierProduction
	database := ports.SharedDatabase("ocel", tier)
	root := "projects/acme-prod/databases/" + database + "/documents/taskstores/"
	fake := &heldFirestore{held: map[string]bool{root + "production~shop/environments/production/runs/e1": true}}
	fake.dropped.Store(1)
	clients := holding(t, fake)
	clients.ProjectDatabases = map[string]string{"shop": database}

	if err := topics.RemoveProjectRuns(context.Background(), clients, tier, "shop"); err != nil {
		t.Fatalf("RemoveProjectRuns() = %v, want the runs listed again once the dropped connection is reopened", err)
	}

	if len(fake.held) != 0 {
		t.Errorf("after removing shop's runs %v are left, want none", slices.Collect(maps.Keys(fake.held)))
	}
}

func TestRemovingAProjectsRunsDeletesEveryRunAndRecordOfItsEnvironmentsAndNoOtherProjects(t *testing.T) {
	t.Parallel()
	tier := environment.TierProduction
	database := ports.SharedDatabase("ocel", tier)
	root := "projects/acme-prod/databases/" + database + "/documents/taskstores/"
	fake := &heldFirestore{held: map[string]bool{}}
	for _, name := range []string{
		"production~shop/environments/production/runs/e1",
		"production~shop/environments/production/records/delay~resize~a",
		"production~shop/environments/pr-7/runs/e2",
		"production~shopx/environments/production/runs/e3",
		"production~blog/environments/production/records/delay~resize~b",
	} {
		fake.held[root+name] = true
	}
	clients := holding(t, fake)
	clients.ProjectDatabases = map[string]string{"shop": database}

	if err := topics.RemoveProjectRuns(context.Background(), clients, tier, "shop"); err != nil {
		t.Fatal(err)
	}

	var left []string
	for name := range fake.held {
		left = append(left, strings.TrimPrefix(name, root))
	}
	slices.Sort(left)
	if want := []string{"production~blog/environments/production/records/delay~resize~b", "production~shopx/environments/production/runs/e3"}; !slices.Equal(left, want) {
		t.Errorf("after removing shop's runs %v are left, want %v", left, want)
	}
	for _, read := range append(slices.Clone(fake.listed), fake.queried...) {
		if !strings.HasPrefix(read, root+"production~shop/") {
			t.Errorf("removing shop's runs read %s, outside shop's own documents", read)
		}
	}
}
