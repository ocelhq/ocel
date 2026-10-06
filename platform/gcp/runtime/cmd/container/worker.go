package main

import (
	"fmt"
	"maps"
	"net/http"
	"os"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	variables "github.com/ocelhq/ocel/platform/gcp/provider/live"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func deploymentOf(manifest variables.Manifest) topics.Deployment {
	declared := make(map[string]*provider.TopicSpec, len(manifest.Tasks.Topics))
	for name, spec := range maps.Clone(manifest.Tasks.Topics) {
		declared[name] = &spec
	}
	return topics.Deployment{
		Clients: &ports.Clients{
			Namespace: provider.Namespace(manifest.Namespace),
			Project:   manifest.Project,
			Region:    manifest.Region,
			Endpoint:  manifest.Endpoint,
		},
		Names: topics.Names{
			Namespace: provider.Namespace(manifest.Namespace),
			Scope:     topics.Scope{Slug: manifest.Slug, Tier: environment.Tier(manifest.Tier), Environment: manifest.Tasks.Environment},
		},
		Declared: declared,
		Delays:   topics.Delays{Queue: manifest.Tasks.DelayQueue, Account: manifest.Tasks.Account, PublishURL: manifest.Tasks.PublishURL},
	}
}

func workerFront(manifest variables.Manifest, worker, upstream string) (http.Handler, error) {
	if manifest.Tasks == nil {
		return nil, fmt.Errorf("this revision runs worker %s, and its deploy pinned no topic or task for it to consume", worker)
	}
	deployment := deploymentOf(manifest)
	return topics.Deliveries{Store: deployment.Store(), Topics: deployment.Declared, Worker: upstream}, nil
}
