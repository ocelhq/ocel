package tunnel

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
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	ContainerName   = "ocel-tunnel"
	Network         = "ocel-tunnel-network"
	Dir             = boxapi.StateRoot + "/tunnel"
	mount           = "/etc/ocel-tunnel"
	Upstream        = "http://" + boxapi.Switchboard + ":" + boxapi.TunnelListenPort
	startAttempts   = 30
	nameSuffixBytes = 4
	rootUser        = "0:0"
	memoryBytes     = 256 << 20
)

type Container struct {
	box     *ssh.Box
	routing *routing.Table
}

func New(box *ssh.Box, routing *routing.Table) *Container {
	return &Container{box: box, routing: routing}
}

func Service(tunnel boxapi.Tunnel, connector edge.TunnelConnector) docker.Service {
	return docker.Service{
		Memory:  memoryBytes,
		Name:    ContainerName,
		Image:   connector.Image,
		Network: Network,
		Command: connector.Command,
		Config:  tunnel.ID,
		Env:     []string{connector.TokenFileEnv + "=" + TokenMounted(tunnel.Name)},
		Binds:   []string{Dir + ":" + mount + ":ro"},
		User:    rootUser,
		Ready:   connector.Ready,
		Unready: "did not report a connection to its edge",
	}
}

func (c *Container) Reserve(ctx context.Context, front edge.Kind, connector edge.TunnelConnector) (boxapi.Tunnel, error) {
	address, err := c.box.Address(ctx)
	if err != nil {
		return boxapi.Tunnel{}, err
	}
	suffix := make([]byte, nameSuffixBytes)
	if _, err := rand.Read(suffix); err != nil {
		return boxapi.Tunnel{}, err
	}
	var reserved boxapi.Tunnel
	err = c.routing.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		switch {
		case state.Tunnel == nil:
			state.Tunnel = &boxapi.Tunnel{
				Edge:                 front,
				Name:                 nameFor(address, hex.EncodeToString(suffix)),
				VisitorAddressHeader: connector.VisitorAddressHeader,
				VisitorSchemeHeader:  connector.VisitorSchemeHeader,
			}
		case state.Tunnel.Edge != front:
			return boxapi.RoutingTable{}, refusal.Refuse(refusal.CodeBusy,
				"%s is reached through the %s edge's tunnel, and one tunnel reaches a box\nRemove `tunnel` from the projects that use it before the %s edge opens one",
				c.box.Named(), state.Tunnel.Edge, front)
		}
		reserved = *state.Tunnel
		return state, nil
	})
	return reserved, err
}

func nameFor(address, suffix string) string {
	spelled := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, strings.ToLower(address))
	return "ocel-" + spelled + "-" + suffix
}

func (c *Container) Run(ctx context.Context, tunnel boxapi.Tunnel, connector edge.TunnelConnector, token func(context.Context) (string, error)) error {
	if tunnel.ID == "" || tunnel.Address == "" {
		return refusal.Refuse(refusal.CodeInvalid, "the tunnel %s names no id or address to run", tunnel.Name)
	}
	if err := c.routing.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		if err := routing.RefuseUnreserved(state, tunnel.Name, c.box.Named()); err != nil {
			return boxapi.RoutingTable{}, err
		}
		state.Tunnel = &tunnel
		return state, nil
	}); err != nil {
		return err
	}
	elevation, err := ssh.Docker, c.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return err
	}
	running, err := c.box.Run(ctx, elevation, "read what "+ContainerName+" runs", Inspecting(), nil)
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
	if _, err := c.box.Run(ctx, elevation, "write the token "+ContainerName+" runs with", shell.Words(TokenArgv("place-secret", tunnel.Name)), strings.NewReader(read)); err != nil {
		return err
	}
	if err := c.start(ctx, tunnel, connector, elevation); err != nil {
		if errors.Is(err, routing.ErrTunnelReleased) {
			return errors.Join(err, c.Stop(ctx, tunnel))
		}
		return err
	}
	return nil
}

func (c *Container) start(ctx context.Context, tunnel boxapi.Tunnel, connector edge.TunnelConnector, elevation ssh.Acting) error {
	for rewrites := 1; ; rewrites++ {
		pair, err := c.routing.CurrentTable(ctx)
		if err != nil {
			return err
		}
		table, err := routing.ReadRoutingTable(pair.Table)
		if err != nil {
			return err
		}
		if err := routing.RefuseUnreserved(table, tunnel.Name, c.box.Named()); err != nil {
			return err
		}
		started := docker.EnsuringNetwork(Network) + "\n" + Service(tunnel, connector).WritingUnderLock(startAttempts, routing.Locked("-x"), routing.ComparedUnder(pair.Digest()))
		result, err := c.box.Stream(ctx, elevation, started, nil)
		switch {
		case err != nil:
			return err
		case result.Code == 0:
			return nil
		case result.Code != routing.ExitMoved:
			return c.box.Refuse("run "+ContainerName, result)
		case rewrites >= routing.Rewrites:
			return c.routing.MovedUnder(pair.Digest(), result)
		}
	}
}

func TokenMounted(name string) string { return mount + "/" + name }

func TokenArgv(subcommand, name string) []string {
	argv := []string{"docker", "run", "--rm", "--interactive", "--network", "none", "--user", rootUser,
		"--env", boxapi.PlaceEnv + "=" + mount,
		"--volume", boxapi.SwitchboardDir + ":" + boxapi.SwitchboardMount + ":ro",
		"--volume", Dir + ":" + mount}
	argv = append(argv, docker.Confined(nil, false)...)
	return append(argv, docker.StaticImage, boxapi.SwitchboardMounted, subcommand, TokenMounted(name))
}

func Inspecting() string {
	return "docker inspect --type container --format " +
		shell.Quote(fmt.Sprintf(`{{.State.Running}} {{index .Config.Labels %q}}`, docker.ConfigLabel)) + " " +
		shell.Quote(ContainerName) + " 2>/dev/null || true"
}

func Removing(retired boxapi.Tunnel) string {
	return routing.Locked("-x") +
		"if [ \"$(docker inspect --type container --format " + shell.Quote(fmt.Sprintf(`{{index .Config.Labels %q}}`, docker.ConfigLabel)) + " " +
		shell.Quote(ContainerName) + " 2>/dev/null)\" = " + shell.Quote(retired.ID) + " ]; then\n" +
		"docker rm --force " + shell.Quote(ContainerName) + " >/dev/null 2>&1 || true\n" +
		"fi\n" +
		shell.Words(TokenArgv("unplace", retired.Name))
}

func (c *Container) Release(ctx context.Context) ([]boxapi.Tunnel, error) {
	var retired []boxapi.Tunnel
	var stopped boxapi.Tunnel
	stopping := false
	if err := c.routing.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
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
	return retired, c.Stop(ctx, stopped)
}

func (c *Container) Stop(ctx context.Context, tunnel boxapi.Tunnel) error {
	elevation, err := ssh.Docker, c.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return err
	}
	_, err = c.box.Run(ctx, elevation, "remove "+ContainerName, Removing(tunnel), nil)
	return err
}

func (c *Container) Forget(ctx context.Context, name string) error {
	return c.routing.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		state.Retired = slices.DeleteFunc(slices.Clone(state.Retired), func(held boxapi.Tunnel) bool { return held.Name == name })
		return state, nil
	})
}
