package edges

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Deps struct {
	AWS func(ctx context.Context) (aws.Config, error)
}

var constructors = map[edge.Kind]func(Deps) edge.Edge{
	cloudflare.Kind: func(Deps) edge.Edge { return cloudflare.New() },
	cloudfront.Kind: func(deps Deps) edge.Edge { return cloudfront.New(cloudfront.FromConfig(deps.AWS)) },
	apigateway.Kind: func(deps Deps) edge.Edge { return apigateway.New(apigateway.FromConfig(deps.AWS)) },
}

const DefaultKind = cloudfront.Kind

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
