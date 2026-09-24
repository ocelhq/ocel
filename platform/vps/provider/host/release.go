package host

import (
	"bytes"
	"context"
	"errors"
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
	drainCeiling = "open requests get 502 or a truncated response; websockets and server-sent-events reconnect"
)

type Release struct {
	Apps          []AppRelease
	DeployTimeout time.Duration
	DrainTimeout  time.Duration
	Holding       func(ctx context.Context) error
}

type AppRelease struct {
	RouteKey
	Target     string
	HealthPath string
}

func (a AppRelease) path() string { return "/" + strings.TrimPrefix(a.HealthPath, "/") }

func (a AppRelease) route() AppRoute {
	return AppRoute{RouteKey: a.RouteKey, Upstream: a.Target, Health: a.path()}
}

func (a AppRelease) gate() string { return a.Target + a.path() }

func (a AppRelease) name() string { return containerOf(a.Target) }

func (r Release) apps() string { return listed(r.Apps, func(app AppRelease) string { return app.App }) }

func (r Release) names() string { return listed(r.Apps, AppRelease.name) }

func listed[T any](items []T, name func(T) string) string {
	named := make([]string, 0, len(items))
	for _, item := range items {
		named = append(named, name(item))
	}
	return strings.Join(named, ", ")
}

func containerOf(address string) string {
	name, _, _ := strings.Cut(address, ":")
	return name
}

type Unserved struct{ Err error }

func (u Unserved) Error() string { return u.Err.Error() }

func (u Unserved) Unwrap() error { return u.Err }

func (h *Host) Release(ctx context.Context, rel Release, report providerkit.Reporter) error {
	for _, app := range rel.Apps {
		if strings.TrimSpace(app.HealthPath) == "" {
			return Unserved{providerkit.Refuse(providerkit.CodeInvalid,
				"release %s onto %s: no health check path\nSet %q in your project configuration",
				app.App, h.named(), healthKey)}
		}
	}
	if len(rel.Apps) == 0 {
		return nil
	}
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return Unserved{err}
	}

	gates := make([]string, 0, len(rel.Apps))
	for _, app := range rel.Apps {
		gates = append(gates, app.gate())
	}
	say(report, "Checking "+strings.Join(gates, ", ")+", then flipping the proxy")
	gated, err := h.stream(ctx, words(gateCommand(rel.DeployTimeout, gates)), nil, elevation)
	if err != nil {
		return h.ungated(ctx, rel, "never came back with an exit code", err.Error(), "", elevation)
	}
	if gated.Code != 0 {
		return h.ungated(ctx, rel, fmt.Sprintf("exited %d", gated.Code), strings.TrimSpace(gated.Stderr), gated.Stdout, elevation)
	}

	var cut cutover
	var overtaken error
	if _, err := h.composeProxy(ctx, func(standing ProxyState) (ProxyState, error) {
		if rel.Holding != nil {
			if overtaken = rel.Holding(ctx); overtaken != nil {
				return standing, overtaken
			}
		}
		cut = cutting(rel, standing)
		return cut.flip(standing), nil
	}); err != nil {
		if overtaken != nil {
			return h.overtaken(ctx, rel, overtaken)
		}
		return h.stranded(ctx, rel, cut, err, elevation)
	}

	if report != nil {
		for _, retiree := range cut.retiring {
			report.Detail(fmt.Sprintf("%s has %s to drain, then %s",
				containerOf(retiree), rel.DrainTimeout, drainCeiling))
		}
	}
	flipped, err := h.stream(ctx, words(flipCommand(rel.DrainTimeout, cut.retiring)), nil, elevation)
	if err != nil {
		return h.unflipped(ctx, rel, cut, "never came back with an exit code", err.Error(), elevation)
	}
	if flipped.Code != 0 {
		return h.unflipped(ctx, rel, cut, fmt.Sprintf("exited %d", flipped.Code), strings.TrimSpace(flipped.Stderr), elevation)
	}
	tellDrain(report, flipped.Stdout)
	return h.settle(ctx, rel, cut, report, elevation)
}

func (h *Host) settle(ctx context.Context, rel Release, cut cutover, report providerkit.Reporter, elevation string) error {
	ctx, stop := sparing(ctx)
	defer stop()
	var failed, unstopped []string
	refused := map[string]error{}
	for _, retiree := range cut.retiring {
		say(report, "Stopping "+containerOf(retiree))
		if err := h.StopContainer(ctx, containerOf(retiree)); err != nil {
			unstopped = append(unstopped, retiree)
			refused[retiree] = err
		}
	}
	settled := h.stopped(ctx, unstopped, elevation)
	for _, retiree := range unstopped {
		if !slices.Contains(settled, retiree) {
			failed = append(failed, fmt.Sprintf("%s was drained and unrouted but not stopped, so it is still running: %v", containerOf(retiree), refused[retiree]))
		}
	}
	if _, err := h.composeProxy(ctx, func(standing ProxyState) (ProxyState, error) {
		return cut.settle(standing, h.stopped(ctx, cut.others(standing), elevation)), nil
	}); err != nil {
		failed = append(failed, fmt.Sprintf("the steady-state write failed, so %s still declares %s on %s until the next release on this box clears it: %v",
			ProxyConfig, listed(cut.retiring, containerOf), proxyDrainServer, err))
	} else if _, err := h.ran(ctx, "reload the proxy's steady-state configuration",
		words(helperCommand("flip", ProxyConfigMount)), nil, elevation); err != nil {
		failed = append(failed, fmt.Sprintf("the steady-state reload failed, so the running proxy still declares %s on %s until the next release on this box reloads it: %v",
			listed(cut.retiring, containerOf), proxyDrainServer, err))
	}
	if len(failed) == 0 {
		return nil
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"release %s onto %s: flipped onto %s, which now serve, but what follows the flip did not all finish:\n%s",
		rel.apps(), h.named(), rel.names(), strings.Join(failed, "\n"))
}

func runningCommand(names []string) string {
	return "for name in " + words(names) + `; do ` +
		`if said=$(docker inspect --type container --format '{{.State.Running}}' "$name" 2>&1); then printf '%s %s\n' "$name" "$said"; ` +
		`elif printf '%s' "$said" | grep -qi 'no such'; then printf '%s gone\n' "$name"; fi; done`
}

func (h *Host) stopped(ctx context.Context, upstreams []string, elevation string) []string {
	if len(upstreams) == 0 {
		return nil
	}
	names := make([]string, 0, len(upstreams))
	for _, upstream := range upstreams {
		names = append(names, containerOf(upstream))
	}
	said := h.said(ctx, runningCommand(names), elevation)
	var gone []string
	for line := range strings.Lines(said) {
		fields := strings.Fields(line)
		if len(fields) != 2 || (fields[1] != "false" && fields[1] != "gone") {
			continue
		}
		for _, upstream := range upstreams {
			if containerOf(upstream) == fields[0] {
				gone = append(gone, upstream)
			}
		}
	}
	return gone
}

type cutover struct {
	rel      Release
	prior    []AppRoute
	retiring []string
}

func cutting(rel Release, standing ProxyState) cutover {
	cut := cutover{rel: rel}
	for _, app := range rel.Apps {
		at := slices.IndexFunc(standing.Routes, func(route AppRoute) bool { return route.RouteKey == app.RouteKey })
		if at < 0 {
			continue
		}
		cut.prior = append(cut.prior, standing.Routes[at])
		if upstream := standing.Routes[at].Upstream; upstream != app.Target {
			cut.retiring = append(cut.retiring, upstream)
		}
	}
	slices.Sort(cut.retiring)
	cut.retiring = slices.Compact(cut.retiring)
	return cut
}

func (c cutover) composed() bool { return len(c.rel.Apps) > 0 }

func (c cutover) routed(standing ProxyState) ProxyState {
	standing.Grace = c.rel.DrainTimeout
	for _, app := range c.rel.Apps {
		standing.Routes = Routing(standing.Routes, app.route())
	}
	return standing
}

func (c cutover) flip(standing ProxyState) ProxyState {
	standing = c.routed(standing)
	for _, retiree := range c.retiring {
		if !slices.Contains(standing.Retiring, retiree) {
			standing.Retiring = append(standing.Retiring, retiree)
		}
	}
	return standing
}

func (c cutover) released(retiring []string) []string {
	return slices.DeleteFunc(slices.Clone(retiring), func(held string) bool { return slices.Contains(c.retiring, held) })
}

func (c cutover) others(standing ProxyState) []string { return c.released(standing.Retiring) }

func (c cutover) settle(standing ProxyState, stopped []string) ProxyState {
	standing.Retiring = slices.DeleteFunc(c.released(standing.Retiring), func(held string) bool { return slices.Contains(stopped, held) })
	return standing
}

func (c cutover) back(standing ProxyState) (ProxyState, error) {
	for _, app := range c.rel.Apps {
		ours := func(route AppRoute) bool { return route.RouteKey == app.RouteKey }
		at := slices.IndexFunc(standing.Routes, ours)
		if at < 0 || standing.Routes[at].Upstream != app.Target {
			continue
		}
		standing.Routes = Unrouting(standing.Routes, ours)
		if was := slices.IndexFunc(c.prior, ours); was >= 0 {
			standing.Routes = append(standing.Routes, c.prior[was])
		}
	}
	standing.Retiring = c.released(standing.Retiring)
	return standing, nil
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

const proxyRewrites = 5

type composed struct {
	held    proxyDocument
	written string
	changed bool
}

func (h *Host) composeProxy(ctx context.Context, compose func(ProxyState) (ProxyState, error)) (composed, error) {
	rewrites := 0
	for {
		held, err := h.proxyDocument(ctx)
		if err != nil {
			return composed{}, err
		}
		standing, err := ReadProxyState([]byte(held.text))
		if err != nil {
			return composed{held: held}, err
		}
		next, err := compose(standing)
		if err != nil {
			return composed{held: held}, err
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

func gateCommand(window time.Duration, gates []string) []string {
	return helperCommand(append([]string{"gate", "--deploy-timeout", seconds(window)}, gates...)...)
}

func flipCommand(window time.Duration, retiring []string) []string {
	argv := []string{"flip"}
	if len(retiring) > 0 {
		argv = append(argv, "--drain-timeout", seconds(window))
		for _, retiree := range retiring {
			argv = append(argv, "--retire", retiree)
		}
	}
	return helperCommand(append(argv, ProxyConfigMount)...)
}

func seconds(window time.Duration) string {
	return strconv.Itoa(int(window.Round(time.Second).Seconds()))
}

const unwindWindow = 60 * time.Second

func sparing(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), unwindWindow)
}

func (h *Host) ungated(ctx context.Context, rel Release, outcome, verdict, said, elevation string) error {
	ctx, stop := sparing(ctx)
	defer stop()
	if verdict == "" {
		verdict = "no reason given"
	}
	failed := rel.Apps
	for line := range strings.Lines(said) {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != caddyadmin.Ungated {
			continue
		}
		if at := slices.IndexFunc(rel.Apps, func(app AppRelease) bool { return app.gate() == fields[1] }); at >= 0 {
			failed = rel.Apps[at : at+1]
		}
	}
	var evidence strings.Builder
	for _, app := range failed {
		state := h.said(ctx, stateCommand(app.name()), elevation)
		logs := h.said(ctx, logCommand(app.name()), elevation)
		if logs == "" {
			logs = noLogOutput
		}
		fmt.Fprintf(&evidence, "\ngate: http://%s, %s to answer 2xx (set by %q)\nstate: %s\nlogs (last %s lines): %s",
			app.gate(), rel.DeployTimeout, healthKey, state, appLogTail, logs)
	}
	return Unserved{providerkit.Refuse(providerkit.CodeNotReady,
		"release %s onto %s: the gate %s; the previous release is still live\n%s%s%s",
		rel.apps(), h.named(), outcome, verdict, evidence.String(), h.discard(ctx, rel))}
}

func (h *Host) overtaken(ctx context.Context, rel Release, why error) error {
	ctx, stop := sparing(ctx)
	defer stop()
	return Unserved{fmt.Errorf("release %s onto %s: %w; nothing was written, so the box serves what it served before%s",
		rel.apps(), h.named(), why, h.discard(ctx, rel))}
}

func (h *Host) stranded(ctx context.Context, rel Release, cut cutover, why error, elevation string) error {
	ctx, stop := sparing(ctx)
	defer stop()
	code := providerkit.CodeNotReady
	rolled := ProxyConfig + " untouched"
	if moved(why) {
		code = providerkit.CodeBusy
	} else if restored, err := h.putBack(ctx, cut, elevation); err != nil {
		return providerkit.Refuse(code,
			"release %s onto %s: could not write %s: %v\n%s not restored: %v\n%s left standing",
			rel.apps(), h.named(), ProxyConfig, why, ProxyConfig, err, rel.names())
	} else if restored {
		rolled = ProxyConfig + " restored"
	}
	return Unserved{providerkit.Refuse(code,
		"release %s onto %s: could not write %s; the proxy was not flipped: %v\n%s%s",
		rel.apps(), h.named(), ProxyConfig, why, rolled, h.discard(ctx, rel))}
}

func (h *Host) unflipped(ctx context.Context, rel Release, cut cutover, outcome, verdict, elevation string) error {
	ctx, stop := sparing(ctx)
	defer stop()
	if verdict == "" {
		verdict = "no reason given"
	}
	if _, err := h.putBack(ctx, cut, elevation); err != nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"release %s onto %s: the flip helper %s; the live release is unknown\n%s\nproxy not restored; %s may be live and were left standing: %v",
			rel.apps(), h.named(), outcome, verdict, rel.names(), err)
	}
	return Unserved{providerkit.Refuse(providerkit.CodeNotReady,
		"release %s onto %s: the flip helper %s; the previous release is still live\n%s%s",
		rel.apps(), h.named(), outcome, verdict, h.discard(ctx, rel))}
}

func (h *Host) putBack(ctx context.Context, cut cutover, elevation string) (bool, error) {
	if !cut.composed() {
		return false, nil
	}
	back, err := h.composeProxy(ctx, cut.back)
	if err != nil || !back.changed {
		return false, err
	}
	_, err = h.ran(ctx, "put the proxy back onto the previous release",
		words(helperCommand("flip", ProxyConfigMount)), nil, elevation)
	return true, err
}

func (h *Host) discard(ctx context.Context, rel Release) string {
	state, _, err := h.proxyState(ctx)
	if err != nil {
		return fmt.Sprintf("\n%s left standing: %s could not be read to tell whether the proxy routes to them: %v",
			rel.names(), ProxyConfig, err)
	}
	live := slices.Clone(state.Retiring)
	for _, route := range state.Routes {
		live = append(live, route.Upstream)
	}
	var left strings.Builder
	for _, app := range rel.Apps {
		if slices.Contains(live, app.Target) {
			continue
		}
		if err := h.RemoveContainer(ctx, app.name()); err != nil {
			fmt.Fprintf(&left, "\n%s left standing: %v", app.name(), err)
			continue
		}
		fmt.Fprintf(&left, "\n%s removed", app.name())
	}
	return left.String()
}
