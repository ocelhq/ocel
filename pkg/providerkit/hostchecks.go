package providerkit

import (
	"context"
	"fmt"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func HostChecksProto(checks []provider.HostCheck) []*contractv1.HostCheck {
	if len(checks) == 0 {
		return nil
	}
	wired := make([]*contractv1.HostCheck, 0, len(checks))
	for _, check := range checks {
		wired = append(wired, &contractv1.HostCheck{
			Subject: check.Subject,
			Verdict: verdictProto(check.Verdict),
			Finding: check.Finding,
			Fix:     check.Fix,
		})
	}
	return wired
}

func verdictProto(verdict provider.HostVerdict) contractv1.HostCheck_Verdict {
	switch verdict {
	case provider.HostOwed:
		return contractv1.HostCheck_VERDICT_OWED
	case provider.HostFail:
		return contractv1.HostCheck_VERDICT_FAIL
	default:
		return contractv1.HostCheck_VERDICT_PASS
	}
}

func (h *handlers) hostChecks(ctx context.Context, p provider.Provider, class edge.Class, hostnames []string) []*contractv1.HostCheck {
	checkHost := p.Hooks().CheckHost
	if checkHost == nil {
		return nil
	}
	checks, err := checkHost(ctx, provider.HostCheckRequest{Class: class, Hostnames: hostnames})
	if err != nil {
		return HostChecksProto([]provider.HostCheck{{
			Verdict: provider.HostFail,
			Finding: fmt.Sprintf("what stands on this box for the life of it could not be read, so none of it was judged: %v", err),
			Fix:     "run `ocel doctor` once the machine answers again",
		}})
	}
	return HostChecksProto(checks)
}
