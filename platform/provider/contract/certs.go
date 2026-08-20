package provider

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Certificate struct {
	ID         string        `json:"id"`
	Hostnames  []string      `json:"hostnames"`
	Validation []edge.Record `json:"validation,omitempty"`
	Issued     bool          `json:"issued"`
}

type Certificates interface {
	Issue(ctx context.Context, hostnames []string) (Certificate, error)
	Describe(ctx context.Context, id string) (Certificate, error)
	Discard(ctx context.Context, id string) error
	Probe(ctx context.Context, hostname string) (bool, error)
}
