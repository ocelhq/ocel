package aws

import (
	"context"
	"maps"

	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

func (p *Provider) ReadNextServerRuntime(context.Context) (map[string][]byte, error) {
	return maps.Clone(payloads.NextServerRuntime()), nil
}
