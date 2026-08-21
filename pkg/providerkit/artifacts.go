package providerkit

import (
	"context"
	"io"
)

type ArtifactStore interface {
	Put(ctx context.Context, ref ArtifactRef, body io.Reader) error

	Open(ctx context.Context, ref ArtifactRef) (io.ReadCloser, error)

	RemovePrefix(ctx context.Context, prefix string, report Reporter) error
}

type ArtifactRef struct {
	Bucket string
	Key    string
}
