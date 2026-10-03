package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	TunnelContainer       = "ocel-tunnel"
	TunnelNetwork         = "ocel-tunnel-network"
	TunnelDir             = live.StateRoot + "/tunnel"
	tunnelMount           = "/etc/ocel-tunnel"
	TunnelService         = "http://" + SwitchboardContainer + ":" + switchboard.TunnelListenPort
	tunnelStartAttempts   = 30
	tunnelNameSuffixBytes = 4
	rootUser              = "0:0"
)

var ErrTunnelReleased = errors.New("the tunnel was released")

type Tunnel struct {
	Edge                 edge.Kind `json:"edge"`
	Name                 string    `json:"name"`
	ID                   string    `json:"id,omitempty"`
	Address              string    `json:"address,omitempty"`
	VisitorAddressHeader string    `json:"visitorAddressHeader,omitempty"`
	VisitorSchemeHeader  string    `json:"visitorSchemeHeader,omitempty"`
}

type tunnelConnector struct {
	image                string
	command              []string
	tokenFileEnv         string
	ready                []string
	visitorAddressHeader string
	visitorSchemeHeader  string
}

var tunnelConnectors = map[edge.Kind]tunnelConnector{
	"cloudflare": {
		image:                "cloudflare/cloudflared@sha256:072c067d25ccbe61d46e18f0d0723255f2bb5304f7317caa95b27031520ff92c",
		command:              []string{"tunnel", "--no-autoupdate", "run"},
		tokenFileEnv:         "TUNNEL_TOKEN_FILE",
		ready:                []string{"cloudflared", "--version"},
		visitorAddressHeader: "Cf-Connecting-Ip",
		visitorSchemeHeader:  "Cf-Visitor",
	},
}

func CanRunTunnelTo(kind edge.Kind) bool {
	_, known := tunnelConnectors[kind]
	return known
}

type TunneledHost struct {
	Hostname string `json:"hostname"`
	Owner    string `json:"owner"`
}

func tunneledHostnames(tunneled []TunneledHost) switchboard.TunneledHostnames {
	hostnames := make([]string, 0, len(tunneled))
	for _, held := range tunneled {
		hostnames = append(hostnames, held.Hostname)
	}
	return switchboard.NewTunneledHostnames(hostnames)
}

func tunnelBox(tunnel Tunnel, connector tunnelConnector) boxContainer {
	return boxContainer{
		name:    TunnelContainer,
		image:   connector.image,
		network: TunnelNetwork,
		command: connector.command,
		config:  tunnel.ID,
		env:     []string{connector.tokenFileEnv + "=" + tunnelTokenMounted(tunnel.Name)},
		binds:   []string{TunnelDir + ":" + tunnelMount + ":ro"},
		user:    rootUser,
		ready:   connector.ready,
		unready: "did not start",
	}
}

func (h *Host) ReserveTunnel(ctx context.Context, front edge.Kind) (Tunnel, error) {
	address, err := h.Address(ctx)
	if err != nil {
		return Tunnel{}, err
	}
	suffix := make([]byte, tunnelNameSuffixBytes)
	if _, err := rand.Read(suffix); err != nil {
		return Tunnel{}, err
	}
	var reserved Tunnel
	err = h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		connector, known := tunnelConnectors[front]
		switch {
		case state.Tunnel == nil && !known:
			return RoutingTable{}, refusal.Refuse(refusal.CodeInvalid,
				"%s runs no tunnel to the %s edge\nRemove `tunnel` from the %s edge's options", h.named(), front, front)
		case state.Tunnel == nil:
			state.Tunnel = &Tunnel{
				Edge:                 front,
				Name:                 tunnelName(address, hex.EncodeToString(suffix)),
				VisitorAddressHeader: connector.visitorAddressHeader,
				VisitorSchemeHeader:  connector.visitorSchemeHeader,
			}
		case state.Tunnel.Edge != front:
			return RoutingTable{}, refusal.Refuse(refusal.CodeBusy,
				"%s is reached through the %s edge's tunnel, and one tunnel reaches a box\nRemove `tunnel` from the projects that use it before the %s edge opens one",
				h.named(), state.Tunnel.Edge, front)
		}
		reserved = *state.Tunnel
		return state, nil
	})
	return reserved, err
}

func tunnelName(address, suffix string) string {
	spelled := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, strings.ToLower(address))
	return "ocel-" + spelled + "-" + suffix
}

func refuseUnreserved(state RoutingTable, name, named string) error {
	if state.Tunnel == nil || state.Tunnel.Name != name {
		return releasedTunnel{refusal.Refuse(refusal.CodeBusy,
			"the tunnel %s no longer reaches %s: another run released it while this one claimed through it\nDeploy again", name, named)}
	}
	return nil
}

type releasedTunnel struct{ error }

func (r releasedTunnel) Unwrap() error { return r.error }

func (releasedTunnel) Is(target error) bool { return target == ErrTunnelReleased }

func (h *Host) RunTunnel(ctx context.Context, tunnel Tunnel, token func(context.Context) (string, error)) error {
	if tunnel.ID == "" || tunnel.Address == "" {
		return refusal.Refuse(refusal.CodeInvalid, "the tunnel %s names no id or address to run", tunnel.Name)
	}
	connector, known := tunnelConnectors[tunnel.Edge]
	if !known {
		return refusal.Refuse(refusal.CodeInvalid, "%s runs no tunnel to the %s edge", h.named(), tunnel.Edge)
	}
	if err := h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		if err := refuseUnreserved(state, tunnel.Name, h.named()); err != nil {
			return RoutingTable{}, err
		}
		state.Tunnel = &tunnel
		return state, nil
	}); err != nil {
		return err
	}
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	running, err := h.ran(ctx, "read what "+TunnelContainer+" runs", renderTunnelInspect(), nil, elevation)
	if err != nil {
		return err
	}
	if strings.TrimSpace(running) == "true "+tunnel.ID {
		return nil
	}
	read, err := token(ctx)
	if err != nil {
		return err
	}
	if _, err := h.ran(ctx, "write the token "+TunnelContainer+" runs with",
		words(renderTunnelTokenArgv("place-secret", tunnel.Name)), strings.NewReader(read), elevation); err != nil {
		return err
	}
	if err := h.startTunnel(ctx, tunnel, connector, elevation); err != nil {
		if errors.Is(err, ErrTunnelReleased) {
			return errors.Join(err, h.StopTunnel(ctx, tunnel))
		}
		return err
	}
	return nil
}

func (h *Host) startTunnel(ctx context.Context, tunnel Tunnel, connector tunnelConnector, elevation string) error {
	for rewrites := 1; ; rewrites++ {
		pair, err := h.currentTable(ctx)
		if err != nil {
			return err
		}
		table, err := ReadRoutingTable(pair.table)
		if err != nil {
			return err
		}
		if err := refuseUnreserved(table, tunnel.Name, h.named()); err != nil {
			return err
		}
		started := networkEnsured(TunnelNetwork) + "\n" + tunnelBox(tunnel, connector).writingUnderRoutingLock(tunnelStartAttempts, comparedUnder(pair.digest()))
		result, err := h.stream(ctx, started, nil, elevation)
		switch {
		case err != nil:
			return err
		case result.Code == 0:
			return nil
		case result.Code != routingMoved:
			return h.refuse("run "+TunnelContainer, result, elevation)
		case rewrites >= routingRewrites:
			return h.movedUnder(pair.digest(), result)
		}
	}
}

func tunnelTokenMounted(name string) string { return tunnelMount + "/" + name }

func renderTunnelTokenArgv(subcommand, name string) []string {
	argv := []string{"docker", "run", "--rm", "--interactive", "--network", "none", "--user", rootUser,
		"--env", switchboard.PlaceEnv + "=" + tunnelMount,
		"--volume", SwitchboardDir + ":" + switchboardMount + ":ro",
		"--volume", TunnelDir + ":" + tunnelMount}
	argv = append(argv, confined(nil, false)...)
	return append(argv, StaticImage, SwitchboardMounted, subcommand, tunnelTokenMounted(name))
}

func renderTunnelInspect() string {
	return "docker inspect --type container --format " +
		quoted(fmt.Sprintf(`{{.State.Running}} {{index .Config.Labels %q}}`, configLabel)) + " " +
		quoted(TunnelContainer) + " 2>/dev/null || true"
}

func renderTunnelRemoval(retired Tunnel) string {
	return routingLocked("-x") +
		"if [ \"$(docker inspect --type container --format " + quoted(fmt.Sprintf(`{{index .Config.Labels %q}}`, configLabel)) + " " +
		quoted(TunnelContainer) + " 2>/dev/null)\" = " + quoted(retired.ID) + " ]; then\n" +
		"docker rm --force " + quoted(TunnelContainer) + " >/dev/null 2>&1 || true\n" +
		"fi\n" +
		words(renderTunnelTokenArgv("unplace", retired.Name))
}

func (h *Host) TunnelHost(ctx context.Context, tunneled TunneledHost, tunnelName string) error {
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		if err := refuseUnreserved(state, tunnelName, h.named()); err != nil {
			return RoutingTable{}, err
		}
		for _, held := range state.Tunneled {
			if held.Hostname == tunneled.Hostname && held.Owner != tunneled.Owner {
				return RoutingTable{}, refusal.Refuse(refusal.CodeBusy,
					"%s is tunneled on this box for %s\nUnbind it there before %s binds it", tunneled.Hostname, held.Owner, tunneled.Owner)
			}
		}
		state.Tunneled = append(dropTunneledHosts(state.Tunneled, func(held TunneledHost) bool { return held.Hostname == tunneled.Hostname }), tunneled)
		return state, nil
	})
}

func (h *Host) RemoveTunneledHost(ctx context.Context, tunneled TunneledHost) error {
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		state.Tunneled = dropTunneledHosts(state.Tunneled, func(held TunneledHost) bool { return held == tunneled })
		return state, nil
	})
}

func dropTunneledHosts(tunneled []TunneledHost, dropped func(TunneledHost) bool) []TunneledHost {
	return slices.DeleteFunc(slices.Clone(tunneled), dropped)
}

func (h *Host) IsTunneled(ctx context.Context, hostname string) (bool, error) {
	state, err := h.routingTable(ctx)
	if err != nil {
		return false, err
	}
	return tunneledHostnames(state.Tunneled).Has(hostname), nil
}

func (h *Host) ReleaseTunnel(ctx context.Context) ([]Tunnel, error) {
	var retired []Tunnel
	var stopped Tunnel
	stopping := false
	if err := h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		stopping = state.Tunnel != nil && len(state.Tunneled) == 0
		if stopping {
			stopped = *state.Tunnel
			state.Retired = append(state.Retired, stopped)
			state.Tunnel = nil
		}
		retired = slices.Clone(state.Retired)
		return state, nil
	}); err != nil || !stopping {
		return retired, err
	}
	return retired, h.StopTunnel(ctx, stopped)
}

func (h *Host) StopTunnel(ctx context.Context, tunnel Tunnel) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	_, err = h.ran(ctx, "remove "+TunnelContainer, renderTunnelRemoval(tunnel), nil, elevation)
	return err
}

func (h *Host) ForgetTunnel(ctx context.Context, name string) error {
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		state.Retired = slices.DeleteFunc(slices.Clone(state.Retired), func(held Tunnel) bool { return held.Name == name })
		return state, nil
	})
}
