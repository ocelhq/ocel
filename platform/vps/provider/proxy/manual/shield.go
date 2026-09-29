package manual

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

func (m Manual) checkShield(ctx context.Context, hostname string) (provider.HostCheck, error) {
	probed := edge.ProbeHostname(hostname)
	answered, failure, err := m.Box.Probe(ctx, probed)
	if err != nil {
		return provider.HostCheck{}, err
	}
	if answered != "" {
		return provider.HostCheck{
			Subject: hostname, Verdict: provider.HostFail,
			Finding: fmt.Sprintf("your proxy answers %s to a client that presents no certificate", hostname),
			Fix:     describeClientCertificateFix(hostname),
		}, nil
	}
	plainAnswered, _, err := m.Box.ProbePlainHTTP(ctx, probed)
	if err != nil {
		return provider.HostCheck{}, err
	}
	if plainAnswered != "" {
		return provider.HostCheck{
			Subject: hostname, Verdict: provider.HostFail,
			Finding: fmt.Sprintf("your proxy forwards %s over plain http, where no client certificate is ever presented", hostname),
			Fix:     fmt.Sprintf("in your proxy, answer %s on port 80 with a redirect to https and forward it nowhere", hostname),
		}, nil
	}
	return provider.HostCheck{
		Subject: hostname, Verdict: provider.HostPass,
		Finding: fmt.Sprintf("your proxy refuses %s to a client that presents no certificate (%s), and forwards it nowhere over plain http", hostname, failure),
	}, nil
}

func describeClientCertificateFix(hostname string) string {
	return fmt.Sprintf("in your proxy, require a client certificate for %s and trust only the ones listed for it under \"shields\" in %s", hostname, live.RoutingTable)
}
