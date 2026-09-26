package providerkit

import (
	"context"
	"io"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type NoArtifacts struct{}

func (NoArtifacts) Put(_ context.Context, ref ArtifactRef, _ io.Reader) error {
	return refusal.Refuse(refusal.CodeInvalid, "this provider keeps no artifact store, so %s has nowhere to go", ref.Key)
}

func (NoArtifacts) Has(context.Context, ArtifactRef) (bool, error) { return false, nil }

func (NoArtifacts) Open(_ context.Context, ref ArtifactRef) (io.ReadCloser, error) {
	return nil, refusal.Refuse(refusal.CodeInvalid, "this provider keeps no artifact store, so there is no artifact at %s", ref.Key)
}

func (NoArtifacts) RemovePrefix(context.Context, edge.Class, string, edge.Progress) error { return nil }

var _ ArtifactStore = NoArtifacts{}
