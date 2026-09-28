package aws

import "github.com/ocelhq/ocel/platform/aws/provider/edges"

func (p *Provider) routers() edges.Routers { return edges.Routers{Deps: p.edgeDeps()} }
