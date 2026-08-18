package dns

import (
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"

	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	KindRoute53    = "route53"
	KindCloudflare = string(edge.KindCloudflare)
)

type Deps struct {
	AWS aws.Config
}

var constructors = map[string]func(Deps, string) (edge.DNSWriter, error){
	KindRoute53: func(deps Deps, zone string) (edge.DNSWriter, error) {
		return NewRoute53(route53.NewFromConfig(deps.AWS), zone), nil
	},
	KindCloudflare: func(_ Deps, zone string) (edge.DNSWriter, error) {
		return cloudflare.NewDNS(zone)
	},
}

func SupportedKinds() []string {
	kinds := make([]string, 0, len(constructors))
	for kind := range constructors {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	return kinds
}

func WriterFor(kind, zone string, deps Deps) (edge.DNSWriter, error) {
	if kind == "" {
		return nil, nil
	}
	construct, ok := constructors[kind]
	if !ok {
		return nil, fmt.Errorf("this provider cannot write DNS records with %q; it writes them with %s", kind, strings.Join(SupportedKinds(), ", "))
	}
	return construct(deps, zone)
}
