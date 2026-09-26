package host

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const listenerCommand = "cat " + listeners.TCPPath + "\n" +
	"if [ -e " + listeners.TCP6Path + " ]; then cat " + listeners.TCP6Path + "; fi"

const holdersCommand = listenerCommand + "\n" +
	"echo '" + listeners.SocketsMark + "'\n" +
	`find /proc/[0-9]*/fd -lname 'socket:\[*' -printf '%h %l\n' 2>/dev/null || true` + "\n" +
	"echo '" + listeners.NamesMark + "'\n" +
	`grep -H '' /proc/[0-9]*/comm 2>/dev/null || true`

func (h *Host) portHolders(ctx context.Context, elevation string) ([]listeners.Listener, error) {
	said, err := h.ran(ctx, "read what listens on this host and what holds it", holdersCommand, nil, elevation)
	if err != nil {
		return nil, err
	}
	return listeners.Parse(strings.NewReader(said))
}

func (h *Host) Listening(ctx context.Context) ([]listeners.Listener, error) {
	said, err := h.ran(ctx, "read what listens on this host", listenerCommand, nil, "")
	if err != nil {
		return nil, err
	}
	return listeners.Parse(strings.NewReader(said))
}

func publishing(port string) []string {
	return []string{"docker", "ps", "--filter", "publish=" + port, "--format", "{{.Names}}"}
}

func (h *Host) Publishing(ctx context.Context, port string) ([]string, error) {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return nil, err
	}
	said, err := h.ran(ctx, "ask which container publishes port "+port, words(publishing(port)), nil, elevation)
	if err != nil {
		return nil, err
	}
	var named []string
	for line := range strings.Lines(said) {
		if name := strings.TrimSpace(line); name != "" {
			named = append(named, name)
		}
	}
	return named, nil
}

func (h *Host) CheckSwitchboard(ctx context.Context, class providerkit.Class) providerkit.HostCheck {
	board := switchboardStanding(nil, h.proxyOption)
	check := providerkit.HostCheck{Subject: board.name, Verdict: providerkit.HostFail,
		Fix: "run `" + providerkit.BootstrapCommand(class) + "` to stand it again"}
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		check.Finding = fmt.Sprintf("ask the engine about %s: %v", board.name, err)
		return check
	}
	result, err := h.stream(ctx, words(board.readiness()), nil, elevation)
	switch {
	case err != nil:
		check.Finding = fmt.Sprintf("ask %s whether it answers: %v", board.name, err)
		return check
	case result.Code == 0:
		check.Verdict, check.Fix = providerkit.HostPass, ""
		check.Finding = fmt.Sprintf("%s is running and answers over its control socket in %s", board.name, switchboard.ControlDir)
		return check
	}
	state := strings.TrimSpace(h.said(ctx, stateCommand(board.name), elevation))
	switch status := stateField(state, "Status"); status {
	case "":
		check.Finding = fmt.Sprintf("no %s container on this box, so nothing routes what the front proxy forwards: %s", board.name, state)
	case "running":
		check.Finding = fmt.Sprintf("%s %s: %s", board.name, board.unready, spoken(result))
	default:
		check.Finding = fmt.Sprintf("%s is %s, so nothing routes what the front proxy forwards: %s", board.name, status, state)
		check.Fix = "check `docker logs " + board.name + "`, then " + check.Fix
	}
	return check
}
