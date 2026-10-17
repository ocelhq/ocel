package gcp

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type deletingRun struct {
	runServer

	mu      sync.Mutex
	deleted []string
}

func (s *deletingRun) open(t *testing.T) *Provider {
	t.Helper()
	serve := s.serve(t)
	return pushing(t, s.identities().serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/services/") && !strings.Contains(r.URL.Path, "/revisions/") {
			s.mu.Lock()
			s.deleted = append(s.deleted, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
			s.mu.Unlock()
		}
		serve(w, r)
	}).endpoint)
}

func recordWorkerRelease(t *testing.T, p *Provider, env, app, release string, workers map[string]string) {
	t.Helper()
	token, err := naming.ParseReleaseToken(release)
	if err != nil {
		t.Fatal(err)
	}
	recorded := stackrecords.Stack{Kind: provider.StackApp, App: app, Functions: []provider.Function{{Name: "server", Physical: app + "-server"}}}
	for worker, service := range workers {
		recorded.Functions = append(recorded.Functions, provider.Function{Name: resources.WorkerName(worker), Physical: service})
	}
	if err := stackrecords.Write(context.Background(), p.KeyValues(), environment.TierProduction, "shop", naming.AppStack(env, app, token), recorded); err != nil {
		t.Fatal(err)
	}
}

func TestRetiringDeletesTheWorkerServicesOfEachAppNoLongerDeployed(t *testing.T) {
	t.Parallel()

	server := &deletingRun{}
	server.service = &run.GoogleCloudRunV2Service{Name: "projects/acme-prod/locations/europe-west1/services/any"}
	p := server.open(t)
	recordWorkerRelease(t, p, "prod", "web", "r00000001", map[string]string{"media": "ocel-shop-prod-w-media"})
	recordWorkerRelease(t, p, "prod", "jobs", "r00000001", map[string]string{"mail": "ocel-shop-prod-w-mail"})
	recordWorkerRelease(t, p, "prod", "jobs", "r00000002", map[string]string{"mail": "ocel-shop-prod-w-mail"})
	recordWorkerRelease(t, p, "staging", "jobs", "r00000001", map[string]string{"mail": "ocel-shop-staging-w-mail"})
	live := provider.LiveApps{Slug: "shop", Tier: environment.TierProduction, Env: "prod", Apps: []string{"web"}}

	plan, err := p.PlanRetirement(context.Background(), live, progress.Discard())
	if err != nil {
		t.Fatalf("PlanRetirement() = %v", err)
	}
	if err := p.Retire(context.Background(), live, progress.Discard()); err != nil {
		t.Fatalf("Retire() = %v", err)
	}

	if !slices.Equal(server.deleted, []string{"ocel-shop-prod-w-mail"}) {
		t.Errorf("deleted %v, want only the worker service of jobs, which production no longer deploys", server.deleted)
	}
	var planned []string
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			if change.Action == provider.ActionDelete {
				planned = append(planned, change.Name)
			}
		}
	}
	if !slices.Equal(planned, []string{"ocel-shop-prod-w-mail"}) {
		t.Errorf("the plan deletes %v, want the one service retiring deletes", planned)
	}
}

func TestRetiringPassesOverAWorkerServiceAlreadyGone(t *testing.T) {
	t.Parallel()

	server := &deletingRun{}
	p := server.open(t)
	recordWorkerRelease(t, p, "prod", "jobs", "r00000001", map[string]string{"mail": "ocel-shop-prod-w-mail"})
	live := provider.LiveApps{Slug: "shop", Tier: environment.TierProduction, Env: "prod"}

	plan, err := p.PlanRetirement(context.Background(), live, progress.Discard())
	if err != nil {
		t.Fatalf("PlanRetirement() = %v", err)
	}
	if err := p.Retire(context.Background(), live, progress.Discard()); err != nil {
		t.Fatalf("Retire() = %v", err)
	}

	if len(plan.Groups) != 0 || len(server.deleted) != 0 {
		t.Errorf("planned %v and deleted %v, want nothing where the service is already gone", plan.Groups, server.deleted)
	}
}
