package providerkit

import (
	"context"
	"fmt"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type HostVerdict int

const (
	HostPass HostVerdict = iota
	HostOwed
	HostFail
)

type HostCheck struct {
	Subject string
	Verdict HostVerdict
	Finding string
	Fix     string
}

type HostCheckRequest struct {
	Class     Class
	Hostnames []string
}

func HostChecksProto(checks []HostCheck) []*contractv1.HostCheck {
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

func verdictProto(verdict HostVerdict) contractv1.HostCheck_Verdict {
	switch verdict {
	case HostOwed:
		return contractv1.HostCheck_VERDICT_OWED
	case HostFail:
		return contractv1.HostCheck_VERDICT_FAIL
	default:
		return contractv1.HostCheck_VERDICT_PASS
	}
}

func (h *handlers) hostChecks(ctx context.Context, provider Provider, class Class, hostnames []string) []*contractv1.HostCheck {
	checkHost := provider.Hooks().CheckHost
	if checkHost == nil {
		return nil
	}
	checks, err := checkHost(ctx, HostCheckRequest{Class: class, Hostnames: hostnames})
	if err != nil {
		return HostChecksProto([]HostCheck{{
			Verdict: HostFail,
			Finding: fmt.Sprintf("what stands on this box for the life of it could not be read, so none of it was judged: %v", err),
			Fix:     "run `ocel doctor` once the machine answers again",
		}})
	}
	return HostChecksProto(checks)
}
