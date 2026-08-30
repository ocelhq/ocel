package host

import (
	"context"
	"errors"
	"fmt"
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
	RouteKey
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
	held, err := h.proxyDocument(ctx)
	if err != nil {
		return err
	}
	standing, err := ReadProxyState([]byte(held.text))
	if err != nil {
		return err
	}
	standing.Grace = rel.DrainTimeout
	standing.Routes = Routing(standing.Routes, AppRoute{RouteKey: rel.RouteKey, Upstream: rel.Target})

	flipping := standing
	flipping.Retiring = rel.Retire
	if rel.Retire == rel.Target {
		flipping.Retiring = ""
	}
	flipped, err := h.writeProxyConfig(ctx, held.digest, flipping)
	if err != nil {
		return h.stranded(ctx, rel, held, err)
	}

	say(report, "Gating "+rel.Target+rel.HealthPath+", then flipping the proxy onto it")
	if flipping.Retiring != "" && report != nil {
		report.Detail(fmt.Sprintf("%s has up to %s to finish what it is still serving, and the deploy returns as soon as it reports nothing in flight. %s. %s",
			rel.retiredName(), rel.DrainTimeout, drainCeiling, hijackedFate))
	}
	result, err := h.stream(ctx, words(releaseCommand(rel)), nil, elevation)
	if err != nil {
		return h.evidence(ctx, rel, "never came back with an exit code", err.Error(), held.text, flipped, elevation)
	}
	if result.Code != 0 {
		return h.evidence(ctx, rel, fmt.Sprintf("exited %d", result.Code), strings.TrimSpace(result.Stderr), held.text, flipped, elevation)
	}
	if flipping.Retiring != "" {
		say(report, "Stopping "+rel.retiredName())
		if err := h.StopContainer(ctx, rel.retiredName()); err != nil {
			return err
		}
	}
	standing.Retiring = ""
	if _, err := h.writeProxyConfig(ctx, flipped, standing); err != nil {
		return err
	}
	if _, err := h.ran(ctx, "reload the proxy's steady-state configuration",
		words(helperCommand("flip", proxyConfigMount)), nil, elevation); err != nil {
		return err
	}
	warnExpiry(report, result.Stdout)
	return nil
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

type proxyDocument struct {
	text   string
	digest string
}

const proxyMoved = 9

func (h *Host) proxyDocument(ctx context.Context) (proxyDocument, error) {
	rendered, err := h.reach(ctx, "read "+ProxyConfig,
		"set -e\nsha256sum "+quoted(ProxyConfig)+" | cut -d' ' -f1\ncat "+quoted(ProxyConfig), nil)
	if err != nil {
		return proxyDocument{}, err
	}
	digest, text, split := strings.Cut(rendered, "\n")
	if !split || strings.TrimSpace(digest) == "" {
		return proxyDocument{}, providerkit.Refuse(providerkit.CodeNotReady,
			"%s on %s answered no digest of its own configuration, and a deploy composes its routes onto the file it read rather than onto whatever the file has become",
			ProxyConfig, h.named())
	}
	return proxyDocument{text: text, digest: strings.TrimSpace(digest)}, nil
}

func (h *Host) writeProxyConfig(ctx context.Context, expected string, state ProxyState) (string, error) {
	rendered, err := RenderProxyConfig(state)
	if err != nil {
		return "", err
	}
	return h.writeProxyDocument(ctx, expected, string(rendered))
}

func (h *Host) writeProxyDocument(ctx context.Context, expected, document string) (string, error) {
	result, err := h.stream(ctx, stagedWrite(expected), strings.NewReader(document), h.reaching(ctx))
	if err != nil {
		return "", err
	}
	switch result.Code {
	case 0:
		return strings.TrimSpace(result.Stdout), nil
	case proxyMoved:
		return "", providerkit.Refuse(providerkit.CodeBusy,
			"%s on %s now reads as %s and this deploy composed its routes onto %s, so another deploy or an `ocel domain` on this box rewrote the whole machine's proxy configuration while this one was working. Nothing was written and what that deploy left is still what the proxy serves: run this one again",
			ProxyConfig, h.named(), strings.TrimSpace(result.Stderr), expected)
	default:
		return "", h.refuse("write "+ProxyConfig, result)
	}
}

func stagedWrite(expected string) string {
	config := quoted(ProxyConfig)
	return strings.Join([]string{
		"set -e",
		"test -f " + config,
		`staged=$(mktemp ` + quoted(ProxyConfig+".XXXXXX") + `)`,
		`trap 'rm -f "$staged"' EXIT`,
		`cat > "$staged"`,
		`held=$(sha256sum ` + config + ` | cut -d' ' -f1)`,
		`if [ "$held" != ` + quoted(expected) + ` ]; then printf '%s' "$held" >&2; exit ` + strconv.Itoa(proxyMoved) + `; fi`,
		`chmod --reference=` + config + ` "$staged"`,
		`chown --reference=` + config + ` "$staged"`,
		`sha256sum "$staged" | cut -d' ' -f1`,
		`mv "$staged" ` + config,
		"trap - EXIT",
	}, "\n")
}

func moved(err error) bool {
	var refusal providerkit.Refusal
	return errors.As(err, &refusal) && refusal.Code == providerkit.CodeBusy
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

func (h *Host) stranded(ctx context.Context, rel Release, held proxyDocument, why error) error {
	rolled := ProxyConfig + " was put back as it was"
	switch {
	case moved(why):
		rolled = ProxyConfig + " was never written and holds what the deploy that moved it wrote"
	default:
		if _, err := h.writeProxyDocument(ctx, held.digest, held.text); err != nil {
			rolled = fmt.Sprintf("%s could not be put back either, which is what a restarted proxy would then serve: %v", ProxyConfig, err)
		}
	}
	left := rel.targetName() + " was removed"
	if err := h.RemoveContainer(ctx, rel.targetName()); err != nil {
		left = fmt.Sprintf("%s is left standing and serving nothing: %v", rel.targetName(), err)
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"release %s onto %s: %s could not be written, so the proxy was never asked to flip and serves what it served before: %v\n%s\n%s",
		rel.App, h.named(), ProxyConfig, why, rolled, left)
}

func (h *Host) evidence(ctx context.Context, rel Release, outcome, verdict, previous, expected, elevation string) error {
	if verdict == "" {
		verdict = "it said nothing about why"
	}
	state := h.said(ctx, stateCommand(rel.targetName()), elevation)
	logs := h.said(ctx, logCommand(rel.targetName()), elevation)
	if logs == "" {
		logs = noLogOutput
	}

	unwound := h.unwind(ctx, rel, previous, expected, elevation)

	return providerkit.Refuse(providerkit.CodeNotReady,
		"release %s onto %s: the proxy's flip helper %s and %s\n"+
			"%s\n"+
			"gate: http://%s%s, %s to answer 2xx; the path is %q in your project configuration\n"+
			"state: %s\n"+
			"logs (last %s lines): %s",
		rel.App, h.named(), outcome, unwound.live, verdict,
		rel.Target, rel.HealthPath, rel.DeployTimeout, healthKey,
		state, appLogTail, logs+unwound.String())
}

func (h *Host) restore(ctx context.Context, previous, expected, elevation string) error {
	if _, err := h.writeProxyDocument(ctx, expected, previous); err != nil {
		return err
	}
	_, err := h.ran(ctx, "put the proxy back onto the previous release",
		words(helperCommand("flip", proxyConfigMount)), nil, elevation)
	return err
}

type aftermath struct {
	live string
	left []string
}

func (a aftermath) String() string {
	if len(a.left) == 0 {
		return ""
	}
	return "\n" + strings.Join(a.left, "\n")
}

func (h *Host) unwind(ctx context.Context, rel Release, previous, expected, elevation string) aftermath {
	after := aftermath{live: "the previous release is still the live upstream"}
	if err := h.restore(ctx, previous, expected, elevation); err != nil {
		after.live = "which release is the live upstream is no longer known here"
		after.left = append(after.left, fmt.Sprintf("%s and the running proxy were not put back, so %s may still be the live upstream and is left standing rather than removed: %v",
			ProxyConfig, rel.targetName(), err))
	} else if err := h.RemoveContainer(ctx, rel.targetName()); err != nil {
		after.left = append(after.left, fmt.Sprintf("%s is still standing and serving nothing: %v", rel.targetName(), err))
	}
	return after
}
