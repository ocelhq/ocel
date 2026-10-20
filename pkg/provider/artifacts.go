package provider

import (
	"context"
	"io"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
)

type ArtifactStore interface {
	Put(ctx context.Context, ref ArtifactRef, body io.Reader) error

	Has(ctx context.Context, ref ArtifactRef) (bool, error)

	Open(ctx context.Context, ref ArtifactRef) (io.ReadCloser, error)

	RemoveObjects(ctx context.Context, tier environment.Tier, scope naming.Coordinate, kinds []naming.ObjectKind, progress progress.Log) error
}

type ArtifactRef struct {
	Tier environment.Tier `json:"tier,omitempty"`
	Key  string           `json:"key,omitempty"`
}
