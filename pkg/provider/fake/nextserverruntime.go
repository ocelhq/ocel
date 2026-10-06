package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/images"
)

const NextServerAdapter = "the fake Next server adapter"

func (p *Provider) ReadNextServerRuntime(context.Context) (map[string][]byte, error) {
	return map[string][]byte{images.NextServerAdapterFile: []byte(NextServerAdapter)}, nil
}
