package bootstrapplan

import (
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func RequiredParts(offered []provider.Part, edgeKind edge.Kind) []string {
	if edgeKind == edge.None {
		return nil
	}
	var required []string
	for _, part := range offered {
		if slices.Contains(part.Edges, edgeKind) {
			required = append(required, part.Name)
		}
	}
	return required
}

func PartFrontingEdge(offered []provider.Part, kind edge.Kind) string {
	for _, part := range offered {
		if slices.Contains(part.Edges, kind) {
			return part.Name
		}
	}
	return ""
}

func PartsToRemove(offered []provider.Part, installed, named []string) ([]string, error) {
	var removing []string
	for _, name := range named {
		if !slices.ContainsFunc(offered, func(part provider.Part) bool { return part.Name == name }) {
			return nil, unknownPart(offered, name)
		}
		if slices.Contains(installed, name) {
			removing = append(removing, name)
		}
	}
	return InOfferedOrder(offered, removing), nil
}

func MissingParts(installed, required []string) []string {
	var out []string
	for _, name := range required {
		if !slices.Contains(installed, name) && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func InOfferedOrder(offered []provider.Part, chosen []string) []string {
	var out []string
	for _, part := range offered {
		if slices.Contains(chosen, part.Name) {
			out = append(out, part.Name)
		}
	}
	return out
}

func partNames(offered []provider.Part) []string {
	out := make([]string, 0, len(offered))
	for _, part := range offered {
		out = append(out, part.Name)
	}
	return out
}

func unknownPart(offered []provider.Part, name string) error {
	names := strings.Join(partNames(offered), ", ")
	if names == "" {
		return refusal.Refuse(refusal.CodeInvalid, "this provider has no bootstrap part named %q, and it adds none beyond the base", name)
	}
	return refusal.Refuse(refusal.CodeInvalid, "this provider has no bootstrap part named %q; it adds %s", name, names)
}
