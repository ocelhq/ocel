package host

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const listenerCommand = "cat " + listeners.TCPPath + "\n" +
	"if [ -e " + listeners.TCP6Path + " ]; then cat " + listeners.TCP6Path + "; fi"

const ownersCommand = listenerCommand + "\n" +
	"echo '" + listeners.SocketsMark + "'\n" +
	`find /proc/[0-9]*/fd -lname 'socket:\[*' -printf '%h %l\n' 2>/dev/null || true` + "\n" +
	"echo '" + listeners.NamesMark + "'\n" +
	`grep -H '' /proc/[0-9]*/comm 2>/dev/null || true`

func (h *Host) portOwners(ctx context.Context, elevation ssh.Acting) ([]listeners.Listener, error) {
	said, err := h.Run(ctx, elevation, "read what listens on this host and which process owns it", ownersCommand, nil)
	if err != nil {
		return nil, err
	}
	return listeners.Parse(strings.NewReader(said))
}

func (h *Host) Listening(ctx context.Context) ([]listeners.Listener, error) {
	said, err := h.Run(ctx, ssh.Login, "read what listens on this host", listenerCommand, nil)
	if err != nil {
		return nil, err
	}
	return listeners.Parse(strings.NewReader(said))
}

func publishing(port string) []string {
	return []string{"docker", "ps", "--filter", "publish=" + port, "--format", "{{.Names}}"}
}

func (h *Host) Publishing(ctx context.Context, port string) ([]string, error) {
	elevation, err := ssh.Docker, h.Assume(ctx, ssh.Docker)
	if err != nil {
		return nil, err
	}
	said, err := h.Run(ctx, elevation, "ask which container publishes port "+port, shell.Words(publishing(port)), nil)
	if err != nil {
		return nil, err
	}
	return publishers(said), nil
}

func publishers(said string) []string { return strings.Fields(said) }

func (h *Host) CheckSwitchboard(ctx context.Context, tier environment.Tier) provider.HostCheck {
	board := switchboardBox(nil, h.proxyOption)
	check := provider.HostCheck{Subject: board.name, Verdict: provider.HostFail,
		Fix: "run `" + provider.BootstrapCommand(tier) + "` to start it again"}
	elevation, err := ssh.Docker, h.Assume(ctx, ssh.Docker)
	if err != nil {
		check.Finding = fmt.Sprintf("ask the engine about %s: %v", board.name, err)
		return check
	}
	result, err := h.Stream(ctx, elevation, shell.Words(board.readiness()), nil)
	switch {
	case err != nil:
		check.Finding = fmt.Sprintf("ask %s whether it answers: %v", board.name, err)
		return check
	case result.Code == 0:
		check.Verdict, check.Fix = provider.HostPass, ""
		check.Finding = fmt.Sprintf("%s is running and answers over its control socket in %s", board.name, boxapi.SwitchboardControlDir)
		if at, restored := h.restoredAt(ctx, elevation); restored {
			check.Finding += fmt.Sprintf("; a deploy started it again at %s after it was removed, as a prune removes it whenever it is stopped", at)
		}
		return check
	}
	state := strings.TrimSpace(h.Evidence(ctx, elevation, stateCommand(board.name)))
	switch status := stateField(state, "Status"); status {
	case "":
		check.Finding = fmt.Sprintf("no %s container on this box, so nothing routes what the front proxy forwards: %s", board.name, state)
	case "running":
		check.Finding = fmt.Sprintf("%s %s: %s", board.name, board.unready, ssh.Spoken(result))
	default:
		check.Finding = fmt.Sprintf("%s is %s, so nothing routes what the front proxy forwards: %s", board.name, status, state)
		check.Fix = "check `docker logs " + board.name + "`, then " + check.Fix
	}
	return check
}
