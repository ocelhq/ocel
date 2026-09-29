package resources

import (
	"context"
	"io"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type NoArtifacts struct{}

func (NoArtifacts) Put(_ context.Context, ref provider.ArtifactRef, _ io.Reader) error {
	return refusal.Refuse(refusal.CodeInvalid, "this provider keeps no artifact store, so %s has nowhere to go", ref.Key)
}

func (NoArtifacts) Has(context.Context, provider.ArtifactRef) (bool, error) { return false, nil }

func (NoArtifacts) Open(_ context.Context, ref provider.ArtifactRef) (io.ReadCloser, error) {
	return nil, refusal.Refuse(refusal.CodeInvalid, "this provider keeps no artifact store, so there is no artifact at %s", ref.Key)
}

func (NoArtifacts) RemovePrefix(context.Context, environment.Tier, string, progress.Log) error {
	return nil
}

var _ provider.ArtifactStore = NoArtifacts{}
