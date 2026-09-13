package manifestwire

import (
	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func Runtime(runtime projectconfig.Runtime) manifestbuilder.Runtime {
	return manifestbuilder.Runtime{Name: runtime.Name, Arch: runtime.Arch}
}

func Declarations(configDir string, resources []declare.Resource) []manifestbuilder.Declaration {
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

func References(configDir string, references []declare.Reference) []manifestbuilder.Reference {
	out := make([]manifestbuilder.Reference, len(references))
	for i, r := range references {
		var source string
		if site, ok := attribution.DeclaringSite(configDir, r.Source); ok {
			source = site.String()
		}
		out[i] = manifestbuilder.Reference{Type: r.Type, Name: r.Name, Source: source}
	}
	return out
}

func Bindings(bindings []projectconfig.Binding) []manifestbuilder.Binding {
	out := make([]manifestbuilder.Binding, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, manifestbuilder.Binding{Type: b.Type, Name: b.Name, External: b.External})
	}
	return out
}
