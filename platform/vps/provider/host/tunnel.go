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
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	TunnelContainer       = "ocel-tunnel"
	TunnelNetwork         = "ocel-tunnel-network"
	TunnelDir             = boxapi.StateRoot + "/tunnel"
	tunnelMount           = "/etc/ocel-tunnel"
	TunnelService         = "http://" + SwitchboardContainer + ":" + boxapi.TunnelListenPort
	tunnelStartAttempts   = 30
	tunnelNameSuffixBytes = 4
	rootUser              = "0:0"
)

var ErrTunnelReleased = errors.New("the tunnel was released")

type Tunnel = boxapi.Tunnel

func tunnelBox(tunnel Tunnel, connector edge.TunnelConnector) boxContainer {
	return boxContainer{
		name:    TunnelContainer,
		image:   connector.Image,
		network: TunnelNetwork,
		command: connector.Command,
		config:  tunnel.ID,
		env:     []string{connector.TokenFileEnv + "=" + tunnelTokenMounted(tunnel.Name)},
		binds:   []string{TunnelDir + ":" + tunnelMount + ":ro"},
		user:    rootUser,
		ready:   connector.Ready,
		unready: "did not report a connection to its edge",
	}
}

func (h *Host) ReserveTunnel(ctx context.Context, front edge.Kind, connector edge.TunnelConnector) (Tunnel, error) {
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
		switch {
		case state.Tunnel == nil:
			state.Tunnel = &Tunnel{
				Edge:                 front,
				Name:                 tunnelName(address, hex.EncodeToString(suffix)),
				VisitorAddressHeader: connector.VisitorAddressHeader,
				VisitorSchemeHeader:  connector.VisitorSchemeHeader,
			}
		case state.Tunnel.Edge != front:
			return RoutingTable{}, refusal.Refuse(refusal.CodeBusy,
				"%s is reached through the %s edge's tunnel, and one tunnel reaches a box\nRemove `tunnel` from the projects that use it before the %s edge opens one",
				h.Named(), state.Tunnel.Edge, front)
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

func (h *Host) RunTunnel(ctx context.Context, tunnel Tunnel, connector edge.TunnelConnector, token func(context.Context) (string, error)) error {
	if tunnel.ID == "" || tunnel.Address == "" {
		return refusal.Refuse(refusal.CodeInvalid, "the tunnel %s names no id or address to run", tunnel.Name)
	}
	if err := h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		if err := refuseUnreserved(state, tunnel.Name, h.Named()); err != nil {
			return RoutingTable{}, err
		}
		state.Tunnel = &tunnel
		return state, nil
	}); err != nil {
		return err
	}
	elevation, err := ssh.Docker, h.Assume(ctx, ssh.Docker)
	if err != nil {
		return err
	}
	running, err := h.Run(ctx, elevation, "read what "+TunnelContainer+" runs", renderTunnelInspect(), nil)
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
	if _, err := h.Run(ctx, elevation, "write the token "+TunnelContainer+" runs with", shell.Words(renderTunnelTokenArgv("place-secret", tunnel.Name)), strings.NewReader(read)); err != nil {
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

func (h *Host) startTunnel(ctx context.Context, tunnel Tunnel, connector edge.TunnelConnector, elevation ssh.Acting) error {
	for rewrites := 1; ; rewrites++ {
		pair, err := h.currentTable(ctx)
		if err != nil {
			return err
		}
		table, err := ReadRoutingTable(pair.table)
		if err != nil {
			return err
		}
		if err := refuseUnreserved(table, tunnel.Name, h.Named()); err != nil {
			return err
		}
		started := networkEnsured(TunnelNetwork) + "\n" + tunnelBox(tunnel, connector).writingUnderLock(tunnelStartAttempts, routingLocked("-x"), comparedUnder(pair.digest()))
		result, err := h.Stream(ctx, elevation, started, nil)
		switch {
		case err != nil:
			return err
		case result.Code == 0:
			return nil
		case result.Code != routingMoved:
			return h.Refuse("run "+TunnelContainer, result)
		case rewrites >= routingRewrites:
			return h.movedUnder(pair.digest(), result)
		}
	}
}

func tunnelTokenMounted(name string) string { return tunnelMount + "/" + name }

func renderTunnelTokenArgv(subcommand, name string) []string {
	argv := []string{"docker", "run", "--rm", "--interactive", "--network", "none", "--user", rootUser,
		"--env", boxapi.PlaceEnv + "=" + tunnelMount,
		"--volume", SwitchboardDir + ":" + switchboardMount + ":ro",
		"--volume", TunnelDir + ":" + tunnelMount}
	argv = append(argv, confined(nil, false)...)
	return append(argv, StaticImage, SwitchboardMounted, subcommand, tunnelTokenMounted(name))
}

func renderTunnelInspect() string {
	return "docker inspect --type container --format " +
		shell.Quote(fmt.Sprintf(`{{.State.Running}} {{index .Config.Labels %q}}`, configLabel)) + " " +
		shell.Quote(TunnelContainer) + " 2>/dev/null || true"
}

func renderTunnelRemoval(retired Tunnel) string {
	return routingLocked("-x") +
		"if [ \"$(docker inspect --type container --format " + shell.Quote(fmt.Sprintf(`{{index .Config.Labels %q}}`, configLabel)) + " " +
		shell.Quote(TunnelContainer) + " 2>/dev/null)\" = " + shell.Quote(retired.ID) + " ]; then\n" +
		"docker rm --force " + shell.Quote(TunnelContainer) + " >/dev/null 2>&1 || true\n" +
		"fi\n" +
		shell.Words(renderTunnelTokenArgv("unplace", retired.Name))
}

func (h *Host) ClaimThrough(ctx context.Context, tunnelName string, claims []HostClaim) error {
	if len(claims) == 0 {
		return nil
	}
	through := make([]HostClaim, 0, len(claims))
	for _, claim := range claims {
		claim.Tunneled = true
		through = append(through, claim)
	}
	if err := validTable(RoutingTable{Claims: through}); err != nil {
		return err
	}
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		if err := refuseUnreserved(state, tunnelName, h.Named()); err != nil {
			return RoutingTable{}, err
		}
		for _, claim := range through {
			taken, err := Claiming(state.Claims, claim)
			if err != nil {
				return RoutingTable{}, err
			}
			state.Claims = taken
		}
		return state, nil
	})
}

func (h *Host) InstallPreviewEntryThrough(ctx context.Context, base, tunnelName string) error {
	return h.installPreviewEntry(ctx, base, func(state RoutingTable) error {
		return refuseUnreserved(state, tunnelName, h.Named())
	})
}

func (h *Host) IsTunneled(ctx context.Context, hostname string) (bool, error) {
	state, err := h.routingTable(ctx)
	if err != nil {
		return false, err
	}
	return state.IsTunneled(hostname), nil
}

func (h *Host) ReleaseTunnel(ctx context.Context) ([]Tunnel, error) {
	var retired []Tunnel
	var stopped Tunnel
	stopping := false
	if err := h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		stopping = state.Tunnel != nil && !state.HasTunneled()
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
	elevation, err := ssh.Docker, h.Assume(ctx, ssh.Docker)
	if err != nil {
		return err
	}
	_, err = h.Run(ctx, elevation, "remove "+TunnelContainer, renderTunnelRemoval(tunnel), nil)
	return err
}

func (h *Host) ForgetTunnel(ctx context.Context, name string) error {
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		state.Retired = slices.DeleteFunc(slices.Clone(state.Retired), func(held Tunnel) bool { return held.Name == name })
		return state, nil
	})
}
