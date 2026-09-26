package caddy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = Builtin{}

func (Builtin) Guarantees() proxy.Guarantees {
	return proxy.Guarantees{OwnsPorts: true}
}

func (Builtin) Render(spec proxy.Spec) ([]byte, error) { return render(spec) }

func (Builtin) File() string { return live.ProxyConfig }

func (Builtin) Unrendered(config []byte, permission proxy.Permission) string {
	return unrendered(config, permission)
}

func (b Builtin) Reload(ctx context.Context) error {
	_, err := b.Box.Ran(ctx, "reload "+Container+" onto "+ConfigMount, reloading())
	return err
}

func (b Builtin) Inspect(ctx context.Context) (proxy.Checks, error) {
	check := provider.HostCheck{Subject: fmt.Sprintf("%s tcp %d", Container, AdminPort)}
	said, err := b.Box.Ran(ctx, "read what listens inside "+Container, listening())
	if err != nil {
		check.Verdict = provider.HostFail
		check.Finding = fmt.Sprintf("read listeners inside %s: %v", Container, err)
		check.Fix = "check the proxy is running"
		return proxy.Checks{check}, nil
	}
	found, err := listeners.Parse(strings.NewReader(said))
	switch {
	case err != nil:
		check.Verdict = provider.HostFail
		check.Finding = fmt.Sprintf("read listeners inside %s: %v", Container, err)
		check.Fix = "check the proxy is running with its own /proc mounted"
	case len(found) == 0:
		check.Verdict = provider.HostFail
		check.Finding = fmt.Sprintf("%s reports no listening sockets; a serving proxy listens on at least %s and %s",
			Container, HTTPPort, HTTPSPort)
		check.Fix = "check the proxy is running with its own /proc mounted"
	case len(listeners.On(found, AdminPort)) > 0:
		check.Verdict = provider.HostFail
		check.Finding = fmt.Sprintf("%s listens on %d inside %s: the admin api is open to anything that reaches the proxy",
			strings.Join(listeners.Lines(listeners.On(found, AdminPort)), ", "), AdminPort, Container)
		check.Fix = "run `ocel bootstrap production` to bind the admin endpoint to " + AdminSocket
	default:
		check.Verdict = provider.HostPass
		check.Finding = fmt.Sprintf("nothing listens on tcp %d inside %s; admin is on %s only", AdminPort, Container, AdminSocket)
	}
	return proxy.Checks{check}, nil
}

func (b Builtin) Certificate(ctx context.Context, hostname string) (proxy.Certificate, error) {
	certificate := proxy.Certificate{Renewal: certs.ProxyRenewal}
	logged, err := b.Box.Said(ctx, logging())
	if err != nil {
		return certificate, err
	}
	if limit, said := certs.RateLimited(logged); said && limit.Covers(hostname) && !limit.Spent(time.Now()) {
		certificate.Trouble = limit.Refusal(hostname)
	}
	return certificate, nil
}
