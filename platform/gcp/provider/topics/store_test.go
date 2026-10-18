package topics_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

func scopeOf(t *testing.T) topics.Scope {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name() + time.Now().String()))
	return topics.Scope{Slug: "shop", Tier: environment.TierPreview, Environment: "t" + hex.EncodeToString(sum[:6])}
}

type committedFirestore struct {
	firestorepb.UnimplementedFirestoreServer

	mu      sync.Mutex
	written []string
}

func (f *committedFirestore) Commit(_ context.Context, req *firestorepb.CommitRequest) (*firestorepb.CommitResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	response := &firestorepb.CommitResponse{CommitTime: timestamppb.Now()}
	for _, write := range req.GetWrites() {
		f.written = append(f.written, write.GetUpdate().GetName())
		response.WriteResults = append(response.WriteResults, &firestorepb.WriteResult{UpdateTime: timestamppb.Now()})
	}
	return response, nil
}

func committingTo(t *testing.T, fake *committedFirestore) *ports.Clients {
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

func databaseOf(document string) string {
	database, _, _ := strings.Cut(document, "/documents/")
	return database
}

func TestTheRunsOfTwoProjectsAreWrittenToDatabasesOfTheirOwnApartFromEveryProjectsValues(t *testing.T) {
	t.Parallel()
	fake := &committedFirestore{}
	clients := committingTo(t, fake)
	now := time.Now()

	for _, project := range []string{"shop", "blog"} {
		store := topics.Store{Clients: clients, Scope: topics.Scope{Slug: project, Tier: environment.TierProduction, Environment: "production"}}
		if _, err := store.CreateRun(context.Background(), runs.Run{Execution: "e1", Topic: "resize", Consumer: "resize", Status: provider.RunQueued, CreatedAt: now, DueAt: now}); err != nil {
			t.Fatalf("CreateRun(%s) = %v", project, err)
		}
	}

	if len(fake.written) != 2 {
		t.Fatalf("the stores wrote %q, want one run each", fake.written)
	}
	shop, blog := databaseOf(fake.written[0]), databaseOf(fake.written[1])
	if want := "projects/acme-prod/databases/" + ports.ProjectWritableDatabase("ocel", environment.TierProduction, "shop"); shop != want {
		t.Errorf("a run of shop is written to %s, want %s, the database shop's apps alone may write", shop, want)
	}
	if shop == blog {
		t.Errorf("the runs of shop and blog are both written to %s, which an app of either may then read and rewrite", shop)
	}
	for project, database := range map[string]string{"shop": shop, "blog": blog} {
		for _, kept := range []string{ports.Database("ocel"), ports.ProjectDatabase("ocel", environment.TierProduction, "shop"), ports.ProjectDatabase("ocel", environment.TierProduction, "blog")} {
			if database == "projects/acme-prod/databases/"+kept {
				t.Errorf("a run of %s is written to %s, which keeps records an app may only read", project, database)
			}
		}
	}
}
