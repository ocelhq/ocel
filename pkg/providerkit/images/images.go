package images

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/naming"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type ImagePush struct {
	App      string
	Source   string
	ImageRef string
	Digest   string

	Function bool
	Built    v1.Image
	Wrap     Wrapped
}

type ImageStore interface {
	Destination() string

	Has(ctx context.Context, push ImagePush) (bool, error)

	Push(ctx context.Context, push ImagePush, progress edge.Progress) error
}

func ImageRef(repository, tag string, target RegistryTarget) string {
	if !target.Named() {
		return repository + ":" + tag
	}
	return target.ImageRef(naming.RepositorySegment(repository), tag)
}
