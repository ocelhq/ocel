package providerserver

import (
	"context"
	"fmt"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func HostChecksProto(checks []provider.HostCheck) []*contractv1.HostCheck {
	if len(checks) == 0 {
		return nil
	}
	encoded := make([]*contractv1.HostCheck, 0, len(checks))
	for _, check := range checks {
		encoded = append(encoded, &contractv1.HostCheck{
			Subject: check.Subject,
			Verdict: verdictProto(check.Verdict),
			Finding: check.Finding,
			Fix:     check.Fix,
		})
	}
	return encoded
}

func verdictProto(verdict provider.HostVerdict) contractv1.HostCheck_Verdict {
	switch verdict {
	case provider.HostNeedsAction:
		return contractv1.HostCheck_VERDICT_NEEDS_ACTION
	case provider.HostFail:
		return contractv1.HostCheck_VERDICT_FAIL
	default:
		return contractv1.HostCheck_VERDICT_PASS
	}
}

func (h *handlers) hostChecks(ctx context.Context, p provider.Provider, req provider.HostCheckRequest) []*contractv1.HostCheck {
	checkHost := p.Hooks().CheckHost
	if checkHost == nil {
		return nil
	}
	checks, err := checkHost(ctx, req)
	if err != nil {
		return HostChecksProto([]provider.HostCheck{{
			Verdict: provider.HostFail,
			Finding: fmt.Sprintf("the current state of this box could not be read, so none of it was judged: %v", err),
			Fix:     "run `ocel doctor` once the machine answers again",
		}})
	}
	return HostChecksProto(checks)
}
