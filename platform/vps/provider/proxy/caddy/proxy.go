package caddy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = Builtin{}

func (Builtin) Guarantees() proxy.Guarantees {
	return proxy.Guarantees{
		OwnsPorts:              true,
		IssuesCertificates:     true,
		ForgetsCertificates:    false,
		HonoursPins:            true,
		ReportsRateLimits:      true,
		ServesPreviewWildcards: true,
	}
}

func (Builtin) Render(admission proxy.Admission) ([]byte, error) { return render(admission) }

func (Builtin) Unrendered(config []byte, admission proxy.Admission) string {
	return unrendered(config, admission)
}

func (b Builtin) Reload(ctx context.Context) error {
	_, err := b.Box.Ran(ctx, "reload "+Container+" onto "+ConfigMount, reloading())
	return err
}

func (b Builtin) Inspect(ctx context.Context) (proxy.Standing, error) {
	check := providerkit.StandingCheck{Subject: fmt.Sprintf("%s tcp %d", Container, AdminPort)}
	said, err := b.Box.Ran(ctx, "read what listens inside "+Container, listening())
	if err != nil {
		check.Verdict = providerkit.StandingFail
		check.Finding = fmt.Sprintf("read listeners inside %s: %v", Container, err)
		check.Fix = "check the proxy is running"
		return proxy.Standing{check}, nil
	}
	held, err := listeners.Parse(strings.NewReader(said))
	switch {
	case err != nil:
		check.Verdict = providerkit.StandingFail
		check.Finding = fmt.Sprintf("read listeners inside %s: %v", Container, err)
		check.Fix = "check the proxy is running with its own /proc mounted"
	case len(held) == 0:
		check.Verdict = providerkit.StandingFail
		check.Finding = fmt.Sprintf("%s reports no listening sockets; a serving proxy holds at least %s and %s",
			Container, HTTPPort, HTTPSPort)
		check.Fix = "check the proxy is running with its own /proc mounted"
	case len(listeners.On(held, AdminPort)) > 0:
		check.Verdict = providerkit.StandingFail
		check.Finding = fmt.Sprintf("%s listens on %d inside %s: the admin api is open to anything that reaches the proxy",
			strings.Join(listeners.Lines(listeners.On(held, AdminPort)), ", "), AdminPort, Container)
		check.Fix = "run `ocel bootstrap production` to bind the admin endpoint to " + AdminSocket
	default:
		check.Verdict = providerkit.StandingPass
		check.Finding = fmt.Sprintf("nothing listens on tcp %d inside %s; admin is on %s only", AdminPort, Container, AdminSocket)
	}
	return proxy.Standing{check}, nil
}

func (b Builtin) Certificate(ctx context.Context, hostname string) (proxy.Certificate, error) {
	held := proxy.Certificate{Renewal: certs.ProxyRenewal}
	logged, err := b.Box.Said(ctx, logging())
	if err != nil {
		return held, err
	}
	if limit, said := certs.RateLimited(logged); said && limit.Covers(hostname) && !limit.Spent(time.Now()) {
		held.Trouble = limit.Refusal(hostname)
	}
	return held, nil
}

func (Builtin) Forget(context.Context, []string) ([]string, error) { return nil, nil }
