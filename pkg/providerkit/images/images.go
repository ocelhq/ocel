package images

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/naming"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Push struct {
	App      string
	Source   string
	ImageRef string
	Digest   string

	Function bool
	Built    v1.Image
	Wrap     Wrapped
}

type Store interface {
	Destination() string

	Has(ctx context.Context, push Push) (bool, error)

	Push(ctx context.Context, push Push, progress edge.Progress) error
}

func Ref(repository, tag string, target Registry) string {
	if !target.Named() {
		return repository + ":" + tag
	}
	return target.ImageRef(naming.RepositorySegment(repository), tag)
}
