package caddy

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = (*Builtin)(nil)

func (b *Builtin) Guarantees() proxy.Guarantees {
	return proxy.Guarantees{
		OwnsPorts:              true,
		IssuesCertificates:     true,
		ForgetsCertificates:    true,
		HonoursPins:            true,
		ReportsRateLimits:      true,
		ServesPreviewWildcards: true,
	}
}

func (b *Builtin) Admit(ctx context.Context, admission proxy.Admission) error {
	if _, err := Render(admission); err != nil {
		return err
	}
	_, err := b.box.Ran(ctx, "reload "+Container+" onto "+ConfigMount, reloading())
	return err
}

func (b *Builtin) Inspect(ctx context.Context) (proxy.Standing, error) {
	check := providerkit.StandingCheck{Subject: fmt.Sprintf("%s tcp %d", Container, AdminPort)}
	said, err := b.box.Ran(ctx, "read what listens inside "+Container, listening())
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

func (b *Builtin) Certificate(ctx context.Context, hostname string) (proxy.Certificate, error) {
	held := proxy.Certificate{Renewal: certs.ProxyRenewal}
	trouble, err := b.Trouble(ctx, hostname)
	if err != nil {
		return held, err
	}
	held.Trouble = trouble
	block, err := b.box.Loopback(ctx, hostname)
	if err != nil || len(block) == 0 {
		return held, err
	}
	leaf, err := certs.Parse("the certificate the proxy serves for "+hostname, block)
	if err != nil {
		return held, err
	}
	held.Served = &leaf
	return held, nil
}

func (b *Builtin) Forget(ctx context.Context, hostnames []string) ([]string, error) {
	if len(hostnames) == 0 {
		return nil, nil
	}
	for _, hostname := range hostnames {
		if !subject(hostname) {
			return nil, providerkit.Refuse(providerkit.CodeInvalid, "%q is not a hostname the proxy holds a certificate for", hostname)
		}
	}
	said, err := b.box.Ran(ctx, "forget what "+Container+" holds for "+strings.Join(hostnames, ", "), forgetting(hostnames))
	if err != nil {
		return nil, err
	}
	var removed []string
	for line := range strings.Lines(said) {
		if taken := strings.TrimSpace(line); taken != "" {
			removed = append(removed, taken)
		}
	}
	return removed, nil
}
