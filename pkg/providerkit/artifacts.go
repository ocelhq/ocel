package providerkit

import (
	"context"
	"io"
)

// ArtifactStore is durable bytes addressed by a key the kit chose. Build output,
// static assets and function payloads all land here. The kit owns every key it
// writes, so the port never parses one.
type ArtifactStore interface {
	Put(ctx context.Context, ref ArtifactRef, body io.Reader) error

	Open(ctx context.Context, ref ArtifactRef) (io.ReadCloser, error)

	// RemovePrefix deletes everything under a prefix. It is how a project's
	// artifacts leave when the project does, and it is the one bulk verb, because
	// deleting ten thousand objects one call at a time is a vendor concern.
	RemovePrefix(ctx context.Context, prefix string, report Reporter) error
}

// ArtifactRef locates one object. Bucket is opaque: the bootstrap made it, the
// records remember it, and the kit passes it through without reading it.
type ArtifactRef struct {
	Bucket string
	Key    string
}
