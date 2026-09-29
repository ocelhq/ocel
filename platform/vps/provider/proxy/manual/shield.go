package manual

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

func (m Manual) shielding(ctx context.Context, hostname string) (provider.HostCheck, error) {
	answered, failure, err := m.Box.Probe(ctx, edge.ProbeHostname(hostname))
	if err != nil {
		return provider.HostCheck{}, err
	}
	if answered == "" {
		return provider.HostCheck{
			Subject: hostname, Verdict: provider.HostPass,
			Finding: fmt.Sprintf("your proxy refuses %s to a client that presents no certificate (%s)", hostname, failure),
		}, nil
	}
	return provider.HostCheck{
		Subject: hostname, Verdict: provider.HostFail,
		Finding: fmt.Sprintf("your proxy answers %s to a client that presents no certificate, and the edge in front forwards it: a request that skips the edge reaches it", hostname),
		Fix:     RequireClientCertificate(hostname),
	}, nil
}

func RequireClientCertificate(hostname string) string {
	return fmt.Sprintf("in your proxy, require a client certificate for %s and trust only the ones its shield lists under \"shields\" in %s", hostname, live.RoutingTable)
}
