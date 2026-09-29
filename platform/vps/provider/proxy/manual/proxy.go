package manual

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = Manual{}

func (Manual) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (Manual) Render(proxy.Spec) ([]byte, error) { return nil, nil }

func (Manual) File() string { return "" }

func (Manual) Unrendered([]byte, proxy.Permission) string { return "" }

func (Manual) RefuseRouted(context.Context, []string) error { return nil }

func (Manual) Validate(context.Context, []byte) error { return nil }

func (Manual) Reload(context.Context, proxy.Spec) error { return nil }

func (m Manual) Inspect(ctx context.Context) (proxy.Checks, error) {
	checks := proxy.Checks{m.portCheck(ctx)}
	claimed, err := m.Box.Claimed(ctx)
	if err != nil {
		return nil, err
	}
	shielded, err := m.Box.Shielded(ctx)
	if err != nil {
		return nil, err
	}
	for _, hostname := range claimed {
		if slices.Contains(shielded, hostname) {
			continue
		}
		check, err := m.routing(ctx, hostname)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	for _, hostname := range shielded {
		check, err := m.checkShield(ctx, hostname)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	return checks, nil
}

func (Manual) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{Renewal: certs.AdoptedRenewal}, nil
}

func (m Manual) RefuseUnshielded(ctx context.Context, hostname string) error {
	check, err := m.checkShield(ctx, hostname)
	if err != nil || check.Verdict == provider.HostPass {
		return err
	}
	return refusal.Refuse(refusal.CodeNotReady, "%s, so the edge in front would forward a hostname anyone reaches without it\n%s, then run this again",
		check.Finding, check.Fix)
}

func (Manual) OriginFiles(proxy.Spec) ([]proxy.OriginFile, error) { return nil, nil }
