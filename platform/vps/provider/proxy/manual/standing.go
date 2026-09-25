package manual

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func (m Manual) holding(ctx context.Context) providerkit.StandingCheck {
	port := strconv.Itoa(httpsPort)
	check := providerkit.StandingCheck{Subject: "tcp " + port, Verdict: providerkit.StandingFail,
		Fix: "start your proxy on " + port + ", routing to " + Loopback(m.Port)}
	publishing, err := m.Box.Publishing(ctx, port)
	if err != nil {
		check.Finding = fmt.Sprintf("ask which container publishes %s: %v", port, err)
		return check
	}
	if slices.Contains(publishing, caddy.Container) {
		check.Finding = fmt.Sprintf("%s, ocel's own proxy, publishes %s on a box whose proxy is yours", caddy.Container, port)
		check.Fix = "run `docker rm -f " + caddy.Container + "` and start your proxy on " + port
		return check
	}
	if len(publishing) > 0 {
		check.Verdict, check.Fix = providerkit.StandingPass, ""
		check.Finding = fmt.Sprintf("%s publishes %s", strings.Join(publishing, ", "), port)
		return check
	}
	held, err := m.Box.Listening(ctx)
	if err != nil {
		check.Finding = fmt.Sprintf("read what listens on this box: %v", err)
		return check
	}
	bound := listeners.On(held, httpsPort)
	if len(bound) == 0 {
		check.Finding = "nothing listens on " + port + "; your proxy serves this box's hostnames there"
		return check
	}
	check.Verdict, check.Fix = providerkit.StandingPass, ""
	check.Finding = fmt.Sprintf("%s listens on %s", strings.Join(listeners.Lines(bound), ", "), port)
	return check
}

func (m Manual) routing(ctx context.Context, hostname string) (providerkit.StandingCheck, error) {
	check := providerkit.StandingCheck{Subject: hostname, Verdict: providerkit.StandingFail, Fix: Route(hostname, m.Port)}
	answered, unreached, err := m.Box.Probe(ctx, hostname)
	switch {
	case err != nil:
		return check, err
	case unreached != "":
		check.Finding = unreached
	case answered != switchboard.EdgeName:
		check.Finding = fmt.Sprintf("%s answers on this box's 443 as %q, not through ocel's switchboard", hostname, answered)
	default:
		check.Verdict, check.Fix = providerkit.StandingPass, ""
		check.Finding = fmt.Sprintf("your proxy routes %s to ocel's switchboard", hostname)
	}
	return check, nil
}
