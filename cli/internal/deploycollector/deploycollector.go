package deploycollector

import (
	"context"
	"net/http"
	"slices"
	"sync"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/resources/v1/resourcesv1connect"
)

type Collector struct {
	*envgate.Gate

	mu         sync.Mutex
	resources  []declare.Resource
	references []declare.Reference
}

func New(gate *envgate.Gate) *Collector {
	return &Collector{Gate: gate}
}

func (c *Collector) Declare(_ context.Context, req *resourcesv1.DeclareRequest) (*resourcesv1.DeclareResponse, error) {
	res, err := declare.Parse(req)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.resources = append(c.resources, res)
	c.mu.Unlock()

	return &resourcesv1.DeclareResponse{}, nil
}

func (c *Collector) Reference(_ context.Context, req *resourcesv1.ReferenceRequest) (*resourcesv1.ReferenceResponse, error) {
	ref, err := declare.ParseReference(req)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.references = append(c.references, ref)
	c.mu.Unlock()

	return &resourcesv1.ReferenceResponse{}, nil
}

func (c *Collector) Snapshot() declare.Collected {
	c.mu.Lock()
	defer c.mu.Unlock()
	return declare.Collected{
		Resources:  slices.Clone(c.resources),
		References: slices.Clone(c.references),
	}
}

func (c *Collector) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	path, handler := resourcesv1connect.NewResourceServiceHandler(c)
	mux.Handle(path, handler)
	mux.HandleFunc("/sync", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}
