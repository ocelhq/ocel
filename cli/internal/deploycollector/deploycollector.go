// Package deploycollector serves a dedicated, minimal Connect
// ResourceService for `ocel deploy`'s discovery phase (OCEL_PHASE=discovery):
// it records the FULL Declare payload — name, type, and typed config — for
// every declared resource, using the shared declare.Parse unit so it can
// never diverge from the dev server in how a Declare is understood. Unlike
// the dev server, it never triggers /sync-driven provisioning and never
// touches cli/internal/devserver, which stays frozen.
package deploycollector

import (
	"context"
	"net/http"
	"sync"

	"github.com/ocelhq/ocel/cli/internal/declare"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/resources/v1/resourcesv1connect"
)

// Collector implements resourcesv1connect.ResourceServiceHandler,
// accumulating every resource declared during a discovery run into a
// builder-ready structure (declare.Resource, config oneof included) for the
// deploy manifest builder to consume.
type Collector struct {
	mu        sync.Mutex
	resources []declare.Resource
}

// New returns an empty Collector.
func New() *Collector {
	return &Collector{}
}

// Declare implements resourcesv1connect.ResourceServiceHandler, parsing req
// via the shared declare.Parse unit and recording its full result —
// including the typed config the dev server itself discards.
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

// Snapshot returns every resource declared so far.
func (c *Collector) Snapshot() []declare.Resource {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]declare.Resource, len(c.resources))
	copy(out, c.resources)
	return out
}

// Mux returns the HTTP handler serving the Connect ResourceService plus a
// no-op /sync acknowledgment. discovery.Bundle's generated entrypoint is
// shared with the dev path and always POSTs /sync once every declaration's
// registration promise resolves; the collector acks it with 200 and does
// nothing else, so discovery completes without any provisioning and without
// the dev server's /sync route ever being involved.
func (c *Collector) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	path, handler := resourcesv1connect.NewResourceServiceHandler(c)
	mux.Handle(path, handler)
	mux.HandleFunc("/sync", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}
