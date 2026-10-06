package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/containerimage"
)

const NextServerAdapter = "the fake Next server adapter"

func (p *Provider) ReadNextServerRuntime(context.Context) (map[string][]byte, error) {
	return map[string][]byte{containerimage.NextServerAdapterFile: []byte(NextServerAdapter)}, nil
}
