package edges

import (
	"fmt"
	"slices"
	"strings"

	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Deps struct{}

var constructors = map[edge.Kind]func(Deps) edge.Edge{
	edge.KindCloudflare: func(Deps) edge.Edge { return cloudflare.New() },
}

func SupportedEdges() []edge.Kind {
	kinds := make([]edge.Kind, 0, len(constructors))
	for kind := range constructors {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	return kinds
}

func EdgeFor(kind edge.Kind, deps Deps) (edge.Edge, error) {
	construct, ok := constructors[kind]
	if !ok {
		return nil, fmt.Errorf("this provider cannot front deployments with the %q edge; it supports %s", kind, supportedList())
	}
	return construct(deps), nil
}

func supportedList() string {
	kinds := SupportedEdges()
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		names = append(names, string(kind))
	}
	return strings.Join(names, ", ")
}
