package manual

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

type PortOwner struct {
	Owner   string
	Trouble string
	Fix     string
}

func PortOwnerOf(ctx context.Context, box Box, port string) (PortOwner, error) {
	publishing, err := box.Publishing(ctx, port)
	if err != nil {
		return PortOwner{}, fmt.Errorf("ask which container publishes %s: %w", port, err)
	}
	if slices.Contains(publishing, proxy.BuiltinContainer) {
		return PortOwner{
			Trouble: fmt.Sprintf("%s, ocel's own proxy, publishes %s on a box whose proxy is yours", proxy.BuiltinContainer, port),
			Fix:     "run `docker rm -f " + proxy.BuiltinContainer + "` and start your proxy on " + port,
		}, nil
	}
	if len(publishing) > 0 {
		return PortOwner{Owner: fmt.Sprintf("%s publishes %s", strings.Join(publishing, ", "), port)}, nil
	}
	found, err := box.Listening(ctx)
	if err != nil {
		return PortOwner{}, fmt.Errorf("read what listens on this box: %w", err)
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		return PortOwner{}, err
	}
	if bound := listeners.On(found, number); len(bound) > 0 {
		return PortOwner{Owner: fmt.Sprintf("%s listens on %s", strings.Join(listeners.Lines(bound), ", "), port)}, nil
	}
	return PortOwner{
		Trouble: "nothing listens on " + port + "; your proxy serves this box's hostnames there",
		Fix:     "start your proxy on " + port,
	}, nil
}

func (m Manual) portCheck(ctx context.Context) provider.HostCheck {
	check := provider.HostCheck{Subject: "tcp " + proxy.HTTPSPort, Verdict: provider.HostFail}
	owner, err := PortOwnerOf(ctx, m.Box, proxy.HTTPSPort)
	switch {
	case err != nil:
		check.Finding, check.Fix = err.Error(), "start your proxy on "+proxy.HTTPSPort+", routing to "+ForwardTo(m.Port)
	case owner.Trouble != "":
		check.Finding, check.Fix = owner.Trouble, owner.Fix+", routing to "+ForwardTo(m.Port)
	default:
		check.Verdict, check.Finding = provider.HostPass, owner.Owner
	}
	return check
}

func (m Manual) routing(ctx context.Context, hostname string) (provider.HostCheck, error) {
	check := provider.HostCheck{Subject: hostname, Verdict: provider.HostFail, Fix: Route(hostname, m.Port)}
	answered, unreached, err := m.Box.Probe(ctx, hostname)
	switch {
	case err != nil:
		return check, err
	case unreached != "":
		check.Finding = unreached
	case answered != switchboard.EdgeName:
		check.Finding = fmt.Sprintf("%s answers on this box's 443 as %q, not through ocel's switchboard", hostname, answered)
	default:
		check.Verdict, check.Fix = provider.HostPass, ""
		check.Finding = fmt.Sprintf("your proxy routes %s to ocel's switchboard", hostname)
	}
	return check, nil
}
