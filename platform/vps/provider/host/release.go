package host

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/caddyadmin"
)

const (
	healthKey    = "health.path"
	appLogTail   = "200"
	noLogOutput  = "(no output)"
	hijackedFate = "neither shape of long-lived connection survives a deploy and clients of both must reconnect, but they reach that end differently: a websocket is cut at the flip itself, while a server-sent-events stream keeps the retired container occupied for this whole window and is cut when it stops, so an app serving one drains at its ceiling on every deploy"
	drainCeiling = "requests still running when the window closes are cut with the retired container: a client still waiting for its first byte receives 502, and one already reading a response sees that response truncated"
)

type Release struct {
	App           string
	Target        string
	Retire        string
	HealthPath    string
	DeployTimeout time.Duration
	DrainTimeout  time.Duration
}

func (r Release) targetName() string {
	name, _, _ := strings.Cut(r.Target, ":")
	return name
}

func (r Release) retiredName() string {
	name, _, _ := strings.Cut(r.Retire, ":")
	return name
}

func (h *Host) Release(ctx context.Context, rel Release, report providerkit.Reporter) error {
	if strings.TrimSpace(rel.HealthPath) == "" {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"release %s onto %s: nothing here names a health check path, and up means a 2xx on the path the wire named rather than on one this provider chose; set %q in your project configuration",
			rel.App, h.named(), healthKey)
	}
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	previous, err := h.reach(ctx, "read "+ProxyConfig, "cat "+quoted(ProxyConfig), nil)
	if err != nil {
		return err
	}
	standing, err := ReadProxyState([]byte(previous))
	if err != nil {
		return err
	}
	standing.Grace = rel.DrainTimeout
	standing.Routes = claiming(standing.Routes, AppRoute{App: rel.App, Upstream: rel.Target})

	flipping := standing
	flipping.Retiring = rel.Retire
	if rel.Retire == rel.Target {
		flipping.Retiring = ""
	}
	if err := h.writeProxyConfig(ctx, flipping); err != nil {
		return err
	}

	say(report, "Gating "+rel.Target+rel.HealthPath+", then flipping the proxy onto it")
	if flipping.Retiring != "" && report != nil {
		report.Detail(fmt.Sprintf("%s has up to %s to finish what it is still serving, and the deploy returns as soon as it reports nothing in flight. %s. %s",
			rel.retiredName(), rel.DrainTimeout, drainCeiling, hijackedFate))
	}
	result, err := h.stream(ctx, words(releaseCommand(rel)), nil, elevation)
	if err != nil {
		return h.evidence(ctx, rel, "never came back with an exit code", err.Error(), previous, elevation)
	}
	if result.Code != 0 {
		return h.evidence(ctx, rel, fmt.Sprintf("exited %d", result.Code), strings.TrimSpace(result.Stderr), previous, elevation)
	}
	if flipping.Retiring != "" {
		say(report, "Stopping "+rel.retiredName())
		if err := h.StopContainer(ctx, rel.retiredName()); err != nil {
			return err
		}
	}
	standing.Retiring = ""
	if err := h.writeProxyConfig(ctx, standing); err != nil {
		return err
	}
	if _, err := h.ran(ctx, "reload the proxy's steady-state configuration",
		words(helperCommand("flip", proxyConfigMount)), nil, elevation); err != nil {
		return err
	}
	warnExpiry(report, result.Stdout)
	return nil
}

func claiming(routes []AppRoute, taken AppRoute) []AppRoute {
	kept := slices.DeleteFunc(slices.Clone(routes), func(route AppRoute) bool { return route.App == taken.App })
	return append(kept, taken)
}

func warnExpiry(report providerkit.Reporter, said string) {
	for line := range strings.Lines(said) {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != caddyadmin.DrainExpired {
			continue
		}
		if report != nil {
			report.Detail(fmt.Sprintf("%s still held %s request(s) when the drain window closed: %s. %s",
				fields[1], fields[2], drainCeiling, hijackedFate))
		}
	}
}

func (h *Host) writeProxyConfig(ctx context.Context, state ProxyState) error {
	rendered, err := RenderProxyConfig(state)
	if err != nil {
		return err
	}
	return h.writeProxyDocument(ctx, string(rendered))
}

func (h *Host) writeProxyDocument(ctx context.Context, document string) error {
	_, err := h.reach(ctx, "write "+ProxyConfig,
		"set -e\ntest -f "+quoted(ProxyConfig)+"\ncat > "+quoted(ProxyConfig), strings.NewReader(document))
	return err
}

func helperCommand(argv ...string) []string {
	return append([]string{"docker", "exec", ProxyContainer, ProxyHelperMount}, argv...)
}

func releaseCommand(rel Release) []string {
	argv := helperCommand("deploy",
		"--target", rel.Target,
		"--health-check-path", rel.HealthPath,
		"--deploy-timeout", seconds(rel.DeployTimeout),
		"--drain-timeout", seconds(rel.DrainTimeout),
		"--config", proxyConfigMount)
	if rel.Retire != "" && rel.Retire != rel.Target {
		argv = append(argv, "--retire", rel.Retire)
	}
	return argv
}

func seconds(window time.Duration) string {
	return strconv.Itoa(int(window.Round(time.Second).Seconds()))
}

func (h *Host) evidence(ctx context.Context, rel Release, outcome, verdict, previous, elevation string) error {
	if verdict == "" {
		verdict = "it said nothing about why"
	}
	state := h.said(ctx, stateCommand(rel.targetName()), elevation)
	logs := h.said(ctx, logCommand(rel.targetName()), elevation)
	if logs == "" {
		logs = noLogOutput
	}

	unwound := h.unwind(ctx, rel, previous, elevation)

	return providerkit.Refuse(providerkit.CodeNotReady,
		"release %s onto %s: the proxy's flip helper %s and the previous release is still the live upstream\n"+
			"%s\n"+
			"gate: http://%s%s, %s to answer 2xx; the path is %q in your project configuration\n"+
			"state: %s\n"+
			"logs (last %s lines): %s",
		rel.App, h.named(), outcome, verdict,
		rel.Target, rel.HealthPath, rel.DeployTimeout, healthKey,
		state, appLogTail, logs+unwound)
}

func (h *Host) restore(ctx context.Context, previous, elevation string) error {
	if err := h.writeProxyDocument(ctx, previous); err != nil {
		return err
	}
	_, err := h.ran(ctx, "put the proxy back onto the previous release",
		words(helperCommand("flip", proxyConfigMount)), nil, elevation)
	return err
}

func (h *Host) unwind(ctx context.Context, rel Release, previous, elevation string) string {
	var left []string
	if err := h.restore(ctx, previous, elevation); err != nil {
		left = append(left, fmt.Sprintf("%s and the running proxy were not put back, so %s may still be the live upstream and is left standing rather than removed: %v",
			ProxyConfig, rel.targetName(), err))
	} else if err := h.RemoveContainer(ctx, rel.targetName()); err != nil {
		left = append(left, fmt.Sprintf("%s is still standing and serving nothing: %v", rel.targetName(), err))
	}
	if len(left) == 0 {
		return ""
	}
	return "\n" + strings.Join(left, "\n")
}
