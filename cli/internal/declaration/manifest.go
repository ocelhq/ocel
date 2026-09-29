package declaration

import (
	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
)

func ToManifest(configDir string, resources []Resource) []manifestbuilder.Declaration {
	decls := make([]manifestbuilder.Declaration, len(resources))
	for i, r := range resources {
		var source string
		if site, ok := attribution.DeclaringSite(configDir, r.Source); ok {
			source = site.String()
		}
		decls[i] = manifestbuilder.Declaration{
			Type:     r.Type,
			Name:     r.Name,
			Postgres: r.Postgres,
			Bucket:   r.Bucket,
			Source:   source,
		}
	}
	return decls
}
