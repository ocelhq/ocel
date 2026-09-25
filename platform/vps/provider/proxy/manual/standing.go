package manual

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

type Holding struct {
	Held    string
	Trouble string
	Fix     string
}

func PortHeld(ctx context.Context, box Box, port string) (Holding, error) {
	publishing, err := box.Publishing(ctx, port)
	if err != nil {
		return Holding{}, fmt.Errorf("ask which container publishes %s: %w", port, err)
	}
	if slices.Contains(publishing, proxy.BuiltinContainer) {
		return Holding{
			Trouble: fmt.Sprintf("%s, ocel's own proxy, publishes %s on a box whose proxy is yours", proxy.BuiltinContainer, port),
			Fix:     "run `docker rm -f " + proxy.BuiltinContainer + "` and start your proxy on " + port,
		}, nil
	}
	if len(publishing) > 0 {
		return Holding{Held: fmt.Sprintf("%s publishes %s", strings.Join(publishing, ", "), port)}, nil
	}
	held, err := box.Listening(ctx)
	if err != nil {
		return Holding{}, fmt.Errorf("read what listens on this box: %w", err)
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		return Holding{}, err
	}
	if bound := listeners.On(held, number); len(bound) > 0 {
		return Holding{Held: fmt.Sprintf("%s listens on %s", strings.Join(listeners.Lines(bound), ", "), port)}, nil
	}
	return Holding{
		Trouble: "nothing listens on " + port + "; your proxy serves this box's hostnames there",
		Fix:     "start your proxy on " + port,
	}, nil
}

func (m Manual) holding(ctx context.Context) providerkit.StandingCheck {
	check := providerkit.StandingCheck{Subject: "tcp " + proxy.HTTPSPort, Verdict: providerkit.StandingFail}
	held, err := PortHeld(ctx, m.Box, proxy.HTTPSPort)
	switch {
	case err != nil:
		check.Finding, check.Fix = err.Error(), "start your proxy on "+proxy.HTTPSPort+", routing to "+ForwardTo(m.Port)
	case held.Trouble != "":
		check.Finding, check.Fix = held.Trouble, held.Fix+", routing to "+ForwardTo(m.Port)
	default:
		check.Verdict, check.Finding = providerkit.StandingPass, held.Held
	}
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
