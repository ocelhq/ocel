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
		decls[i] = manifestbuilder.Declaration{
			Type:     r.Type,
			Name:     r.Name,
			Postgres: r.Postgres,
			Bucket:   r.Bucket,
			Source:   writtenAt(configDir, r.Source),
		}
	}
	return decls
}

func References(configDir string, references []declare.Reference) []manifestbuilder.Reference {
	out := make([]manifestbuilder.Reference, len(references))
	for i, r := range references {
		out[i] = manifestbuilder.Reference{Type: r.Type, Name: r.Name, Source: writtenAt(configDir, r.Source)}
	}
	return out
}

func writtenAt(configDir, source string) string {
	if site, ok := attribution.DeclaringSite(configDir, source); ok {
		return site.String()
	}
	return source
}

func Bindings(bindings []projectconfig.Binding) []manifestbuilder.Binding {
	out := make([]manifestbuilder.Binding, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, manifestbuilder.Binding{Type: b.Type, Name: b.Name, External: b.External})
	}
	return out
}
