package live

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

func AppNetwork(tier environment.Tier, project string) string {
	return "ocel-" + string(tier) + "-" + naming.Sanitize(project)
}
