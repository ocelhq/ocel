package providerkit

import (
	"context"
	"io"
)

type NoArtifacts struct{}

func (NoArtifacts) Put(_ context.Context, ref ArtifactRef, _ io.Reader) error {
	return Refuse(CodeInvalid, "this provider keeps no artifact store, so %s has nowhere to go", ref.Key)
}

func (NoArtifacts) Has(context.Context, ArtifactRef) (bool, error) { return false, nil }

func (NoArtifacts) Open(_ context.Context, ref ArtifactRef) (io.ReadCloser, error) {
	return nil, Refuse(CodeInvalid, "this provider keeps no artifact store, so there is no artifact at %s", ref.Key)
}

func (NoArtifacts) RemovePrefix(context.Context, Class, string, Reporter) error { return nil }

var _ ArtifactStore = NoArtifacts{}
