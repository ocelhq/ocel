package host

import (
	"bytes"
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
	drainCeiling = "open requests get 502 or a truncated response; websockets and server-sent-events reconnect"
)

type Release struct {
	RouteKey
	Target        string
	Retire        string
	HealthPath    string
	DeployTimeout time.Duration
	DrainTimeout  time.Duration
}

func (r Release) targetName() string { return containerOf(r.Target) }

func (r Release) retiredName() string { return containerOf(r.Retire) }

func containerOf(address string) string {
	name, _, _ := strings.Cut(address, ":")
	return name
}

func (h *Host) Release(ctx context.Context, rel Release, report providerkit.Reporter) error {
	if strings.TrimSpace(rel.HealthPath) == "" {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"release %s onto %s: no health check path\nSet %q in your project configuration",
			rel.App, h.named(), healthKey)
	}
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	retiring := rel.Retire
	if rel.Retire == rel.Target {
		retiring = ""
	}
	compose := func(standing ProxyState) ProxyState {
		standing.Grace = rel.DrainTimeout
		standing.Routes = Routing(standing.Routes, AppRoute{RouteKey: rel.RouteKey, Upstream: rel.Target, Health: rel.HealthPath})
		return standing
	}
	flip, err := h.composeProxy(ctx, rel.DrainTimeout, func(standing ProxyState, patient bool) (ProxyState, bool, error) {
		if patient && retiring != "" && standing.Retiring != "" && standing.Retiring != retiring {
			return standing, false, nil
		}
		standing = compose(standing)
		if retiring != "" {
			standing.Retiring = retiring
		}
		return standing, true, nil
	})
	if err != nil {
		return h.stranded(ctx, rel, flip.held, err)
	}
	held, flipped := flip.held, flip.written

	say(report, "Checking "+rel.Target+rel.HealthPath+", then flipping the proxy onto it")
	if retiring != "" && report != nil {
		report.Detail(fmt.Sprintf("%s has %s to drain, then %s",
			rel.retiredName(), rel.DrainTimeout, drainCeiling))
	}
	result, err := h.stream(ctx, words(releaseCommand(rel)), nil, elevation)
	if err != nil {
		return h.evidence(ctx, rel, "never came back with an exit code", err.Error(), held.text, flipped, elevation)
	}
	if result.Code != 0 {
		return h.evidence(ctx, rel, fmt.Sprintf("exited %d", result.Code), strings.TrimSpace(result.Stderr), held.text, flipped, elevation)
	}
	tellDrain(report, result.Stdout)
	if retiring != "" {
		say(report, "Stopping "+rel.retiredName())
		if err := h.StopContainer(ctx, rel.retiredName()); err != nil {
			return err
		}
	}
	if _, err := h.composeProxy(ctx, rel.DrainTimeout, func(standing ProxyState, _ bool) (ProxyState, bool, error) {
		standing = compose(standing)
		if standing.Retiring == retiring {
			standing.Retiring = ""
		}
		return standing, true, nil
	}); err != nil {
		return h.serving(rel, retiring, err)
	}
	if _, err := h.ran(ctx, "reload the proxy's steady-state configuration",
		words(helperCommand("flip", ProxyConfigMount)), nil, elevation); err != nil {
		return h.serving(rel, retiring, err)
	}
	return nil
}

func (h *Host) serving(rel Release, retired string, why error) error {
	left := ProxyConfig + " is one write behind the running proxy"
	if retired != "" {
		left = fmt.Sprintf("%s still routes %s to the stopped %s",
			ProxyConfig, proxyDrainServer, rel.retiredName())
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"release %s onto %s: flipped onto %s, but the follow-up config write failed: %v\n%s; deploy again",
		rel.App, h.named(), rel.targetName(), why, left)
}

func tellDrain(report providerkit.Reporter, said string) {
	if report == nil {
		return
	}
	for line := range strings.Lines(said) {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 3 && fields[0] == caddyadmin.DrainExpired:
			report.Detail(fmt.Sprintf("%s still held %s request(s) when the drain window closed: %s",
				fields[1], fields[2], drainCeiling))
		case len(fields) == 2 && fields[0] == caddyadmin.Drained:
			report.Detail(containerOf(fields[1]) + " reported nothing in flight")
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
			"%s on %s returned no digest",
			ProxyConfig, h.named())
	}
	return proxyDocument{text: text, digest: strings.TrimSpace(digest)}, nil
}

const (
	proxyRewrites = 5
	drainWait     = time.Second
)

type composed struct {
	held    proxyDocument
	written string
	changed bool
}

func (h *Host) composeProxy(ctx context.Context, patience time.Duration, compose func(ProxyState, bool) (ProxyState, bool, error)) (composed, error) {
	rewrites := 0
	deadline := time.Now().Add(patience)
	for {
		held, err := h.proxyDocument(ctx)
		if err != nil {
			return composed{}, err
		}
		standing, err := ReadProxyState([]byte(held.text))
		if err != nil {
			return composed{held: held}, err
		}
		next, ready, err := compose(standing, time.Now().Before(deadline))
		if err != nil {
			return composed{held: held}, err
		}
		if !ready {
			select {
			case <-ctx.Done():
				return composed{held: held}, ctx.Err()
			case <-time.After(drainWait):
			}
			continue
		}
		before, err := RenderProxyConfig(standing)
		if err != nil {
			return composed{held: held}, err
		}
		rendered, err := RenderProxyConfig(next)
		if err != nil {
			return composed{held: held}, err
		}
		if bytes.Equal(before, rendered) {
			return composed{held: held, written: held.digest}, nil
		}
		written, err := h.writeProxyDocument(ctx, held.digest, string(rendered))
		rewrites++
		if err == nil || !moved(err) || rewrites >= proxyRewrites {
			return composed{held: held, written: written, changed: true}, err
		}
	}
}

func (h *Host) writeProxyDocument(ctx context.Context, expected, document string) (string, error) {
	elevation, refused := h.elevate(ctx)
	result, err := h.stream(ctx, stagedWrite(expected), strings.NewReader(document), elevation)
	if err != nil {
		return "", err
	}
	switch result.Code {
	case 0:
		return strings.TrimSpace(result.Stdout), nil
	case proxyMoved:
		return "", providerkit.Refuse(providerkit.CodeBusy,
			"%s on %s changed during this deploy (%s, expected %s); nothing was written\nRun the deploy again",
			ProxyConfig, h.named(), strings.TrimSpace(result.Stderr), expected)
	default:
		return "", unelevated(refused, h.refuse("write "+ProxyConfig, result))
	}
}

func unelevated(refused, why error) error {
	if refused == nil {
		return why
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"%v\ncould not elevate: %v", why, refused)
}

func stagedWrite(expected string) string {
	config := quoted(ProxyConfig)
	return strings.Join([]string{
		"set -e",
		"test -f " + config,
		`staged=$(mktemp ` + quoted(ProxyConfig+".XXXXXX") + `)`,
		`trap 'rm -f "$staged"' EXIT`,
		`cat > "$staged"`,
		"exec 9<" + config,
		"flock -x 9",
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
		"--config", ProxyConfigMount)
	if rel.Retire != "" && rel.Retire != rel.Target {
		argv = append(argv, "--retire", rel.Retire)
	}
	return argv
}

func seconds(window time.Duration) string {
	return strconv.Itoa(int(window.Round(time.Second).Seconds()))
}

const unwindWindow = 60 * time.Second

func sparing(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), unwindWindow)
}

func (h *Host) stranded(ctx context.Context, rel Release, held proxyDocument, why error) error {
	ctx, stop := sparing(ctx)
	defer stop()
	rolled := ProxyConfig + " restored"
	switch {
	case moved(why):
		rolled = ProxyConfig + " untouched"
	default:
		if _, err := h.writeProxyDocument(ctx, held.digest, held.text); err != nil {
			rolled = fmt.Sprintf("%s not restored: %v", ProxyConfig, err)
		}
	}
	left := rel.targetName() + " removed"
	if err := h.RemoveContainer(ctx, rel.targetName()); err != nil {
		left = fmt.Sprintf("%s left standing: %v", rel.targetName(), err)
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"release %s onto %s: could not write %s; the proxy was not flipped: %v\n%s; %s",
		rel.App, h.named(), ProxyConfig, why, rolled, left)
}

func (h *Host) evidence(ctx context.Context, rel Release, outcome, verdict, previous, expected, elevation string) error {
	if verdict == "" {
		verdict = "no reason given"
	}
	state := h.said(ctx, stateCommand(rel.targetName()), elevation)
	logs := h.said(ctx, logCommand(rel.targetName()), elevation)
	if logs == "" {
		logs = noLogOutput
	}

	unwound := h.unwind(ctx, rel, previous, expected, elevation)

	return providerkit.Refuse(providerkit.CodeNotReady,
		"release %s onto %s: the flip helper %s; %s\n"+
			"%s\n"+
			"gate: http://%s%s, %s to answer 2xx (set by %q)\n"+
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
		words(helperCommand("flip", ProxyConfigMount)), nil, elevation)
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
	ctx, stop := sparing(ctx)
	defer stop()
	after := aftermath{live: "the previous release is still live"}
	if err := h.restore(ctx, previous, expected, elevation); err != nil {
		after.live = "the live release is unknown"
		after.left = append(after.left, fmt.Sprintf("proxy not restored; %s may be live and was left standing: %v",
			rel.targetName(), err))
	} else if err := h.RemoveContainer(ctx, rel.targetName()); err != nil {
		after.left = append(after.left, fmt.Sprintf("%s left standing: %v", rel.targetName(), err))
	}
	return after
}
