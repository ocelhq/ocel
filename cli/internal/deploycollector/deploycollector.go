package deploycollector

import (
	"context"
	"net/http"
	"sync"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/resources/v1/resourcesv1connect"
)

type Collector struct {
	*envgate.Gate

	mu        sync.Mutex
	resources []declare.Resource
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

func (c *Collector) Snapshot() []declare.Resource {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]declare.Resource, len(c.resources))
	copy(out, c.resources)
	return out
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
