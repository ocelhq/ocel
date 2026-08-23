package edges

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/pkg/providerkit"
	kit "github.com/ocelhq/ocel/pkg/providerkit/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Deps struct {
	AWS func(ctx context.Context) (aws.Config, error)

	Certificates map[string]string
}

var constructors = map[edge.Kind]func(Deps) edge.Edge{
	cloudflare.Kind: func(Deps) edge.Edge { return cloudflare.New() },
	cloudfront.Kind: func(deps Deps) edge.Edge { return cloudfront.New(cloudfront.FromConfig(deps.AWS)) },
	apigateway.Kind: func(deps Deps) edge.Edge { return apigateway.New(apigateway.FromConfig(deps.AWS)) },
}

const DefaultKind = cloudfront.Kind

type Registry struct {
	Deps Deps
}

var _ providerkit.EdgeRegistry = Registry{}

func (r Registry) Supported() []edge.Kind { return SupportedEdges() }

func (r Registry) Default() edge.Kind { return DefaultKind }

func (r Registry) Open(kind edge.Kind) (edge.Edge, error) {
	construct, ok := constructors[kind]
	if !ok {
		return nil, kit.Refuse(kit.CodeInvalid,
			"this provider cannot front deployments with the %q edge; it supports %s", kind, supportedList())
	}
	return construct(r.Deps), nil
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
	return Registry{Deps: deps}.Open(kind)
}

func (r Registry) Certifier(front edge.Edge, deps certs.Deps) certs.Certifier {
	return certs.CertifierFor(front, deps, r.Deps.Certificates)
}

func IgnoredPinNote(front edge.Edge, certifier certs.Certifier, hostname string) string {
	if !certifier.IgnoresPinFor(hostname) {
		return ""
	}
	return fmt.Sprintf(
		"the certificate pinned for %s is ignored: the %s edge terminates TLS with a certificate of its own, so ocel neither requests nor uses one here",
		hostname, front.Kind(),
	)
}

func supportedList() string {
	kinds := SupportedEdges()
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		names = append(names, string(kind))
	}
	return strings.Join(names, ", ")
}
