package dns

import (
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"

	"github.com/ocelhq/ocel/pkg/providerkit"
	kit "github.com/ocelhq/ocel/pkg/providerkit/ports"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	KindRoute53    = "route53"
	KindCloudflare = string(cloudflare.Kind)
)

type Deps struct {
	AWS aws.Config
}

var constructors = map[string]func(Deps, string) (edge.DNSRecords, error){
	KindRoute53: func(deps Deps, zone string) (edge.DNSRecords, error) {
		return NewRoute53(route53.NewFromConfig(deps.AWS), zone), nil
	},
	KindCloudflare: func(_ Deps, zone string) (edge.DNSRecords, error) {
		return cloudflare.NewDNS(zone)
	},
}

type Registry struct {
	Deps Deps
}

var _ providerkit.DNS = Registry{}

func Kinds() []providerkit.DNSKind {
	kinds := make([]providerkit.DNSKind, 0, len(constructors))
	for _, kind := range SupportedKinds() {
		kinds = append(kinds, providerkit.DNSKind(kind))
	}
	return kinds
}

func (r Registry) Open(kind providerkit.DNSKind, zone string, front edge.Kind) (edge.DNSRecords, error) {
	if kind == KindRoute53 && front == cloudflare.Kind {
		return nil, kit.Refuse(kit.CodeInvalid,
			"route53 cannot write the records a Cloudflare edge answers on — pair a cloudflare edge with cloudflare dns, or drop the edge")
	}
	construct, ok := constructors[string(kind)]
	if !ok {
		return nil, kit.Refuse(kit.CodeInvalid,
			"this provider cannot write DNS records with %q; it writes them with %s", kind, strings.Join(SupportedKinds(), ", "))
	}
	return construct(r.Deps, zone)
}

func SupportedKinds() []string {
	kinds := make([]string, 0, len(constructors))
	for kind := range constructors {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	return kinds
}

func RecordsFor(kind, zone string, deps Deps) (edge.DNSRecords, error) {
	if kind == "" {
		return nil, nil
	}
	return Registry{Deps: deps}.Open(providerkit.DNSKind(kind), zone, "")
}
