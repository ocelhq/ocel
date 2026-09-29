package manifest

import (
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type binding struct {
	Type     resourcesv1.ResourceType
	Name     string
	External string
}

type UndeclaredBindingError struct {
	Type resourcesv1.ResourceType
	Name string
}

func (e *UndeclaredBindingError) Error() string {
	return fmt.Sprintf(
		"`bindings.%s` binds %q, which nothing in this project declares as a %s — a binding names a resource your app already declares, so declare it or drop it",
		naming.ResourceTypeName(e.Type), e.Name, naming.ResourceTypeName(e.Type),
	)
}

type BindingTypeError struct {
	Type     resourcesv1.ResourceType
	Name     string
	Declared resourcesv1.ResourceType
}

func (e *BindingTypeError) Error() string {
	return fmt.Sprintf(
		"`bindings.%s` binds %q, and this project declares %q as a %s — move it to `bindings.%s`, or declare it as a %s",
		naming.ResourceTypeName(e.Type), e.Name, e.Name, naming.ResourceTypeName(e.Declared),
		naming.ResourceTypeName(e.Declared), naming.ResourceTypeName(e.Type),
	)
}

func bindingsOf(configured []projectconfig.Binding) []binding {
	out := make([]binding, 0, len(configured))
	for _, b := range configured {
		out = append(out, binding{Type: b.Type, Name: b.Name, External: b.RecordName()})
	}
	return out
}

func bindBindings(resources []*contractv1.ManifestResource, bindings []binding) error {
	byIdentity := make(map[identity]*contractv1.ManifestResource, len(resources))
	byName := make(map[string]resourcesv1.ResourceType, len(resources))
	for _, r := range resources {
		id := r.GetResource()
		byIdentity[identity{typ: id.GetType(), name: id.GetName()}] = r
		byName[id.GetName()] = id.GetType()
	}

	for _, b := range bindings {
		bound, declared := byIdentity[identity{typ: b.Type, name: b.Name}]
		if !declared {
			if other, named := byName[b.Name]; named {
				return &BindingTypeError{Type: b.Type, Name: b.Name, Declared: other}
			}
			return &UndeclaredBindingError{Type: b.Type, Name: b.Name}
		}
		bound.Binding = b.External
	}
	return nil
}
