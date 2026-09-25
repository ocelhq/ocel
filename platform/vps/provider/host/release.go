package host

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
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
	return AppRoute{RouteKey: a.RouteKey, Upstream: a.Target}
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
	shaped, err := h.composeRouting(ctx, func(standing RoutingTable) (RoutingTable, error) {
		if rel.Holding != nil {
			if overtaken = rel.Holding(ctx); overtaken != nil {
				return standing, overtaken
			}
		}
		cut = cutting(rel, standing)
		return cut.routed(standing), nil
	})
	if err != nil {
		if overtaken != nil {
			return h.overtaken(ctx, rel, overtaken, elevation)
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
	var reloaded error
	if shaped.reloading {
		reloaded = h.reloadedAfterFlip(ctx, shaped)
	}
	return h.settle(ctx, rel, cut, reloaded, report, elevation)
}

func (h *Host) reloadedAfterFlip(ctx context.Context, shaped composed) error {
	refused := h.front.Reload(ctx)
	if refused == nil {
		return nil
	}
	table, err := WriteRoutingTable(shaped.is)
	if err == nil {
		_, err = h.writePair(ctx, shaped.written, routingPair{table: table, config: shaped.prior.config})
	}
	if err != nil {
		return fmt.Errorf("%w\n%s was not put back to what it serves either, so no later deploy reloads it: %w", refused, ProxyConfig, err)
	}
	return refused
}

func (h *Host) settle(ctx context.Context, rel Release, cut cutover, reloaded error, report providerkit.Reporter, elevation string) error {
	ctx, stop := sparing(ctx)
	defer stop()
	var failed, unstopped []string
	if reloaded != nil {
		failed = append(failed, fmt.Sprintf("%s was not reloaded onto %s, so it still terminates what it did before: %v", caddy.Container, ProxyConfig, reloaded))
	}
	idle, err := h.unheld(ctx, cut.retiring, elevation)
	if err != nil {
		for _, retiree := range cut.retiring {
			failed = append(failed, fmt.Sprintf("%s was drained and unrouted but not stopped, so it is still running: %v", containerOf(retiree), err))
		}
	}
	refused := map[string]error{}
	for _, retiree := range idle {
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
	if len(failed) == 0 {
		return nil
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"release %s onto %s: flipped onto %s, which now serve, but what follows the flip did not all finish:\n%s",
		rel.apps(), h.named(), rel.names(), strings.Join(failed, "\n"))
}

func (h *Host) unheld(ctx context.Context, upstreams []string, elevation string) ([]string, error) {
	if len(upstreams) == 0 {
		return nil, nil
	}
	said, err := h.ran(ctx, "ask the proxy whether a route or a drain still holds "+listed(upstreams, containerOf),
		words(idleCommand(upstreams)), nil, elevation)
	if err != nil {
		return nil, err
	}
	state, err := h.routingTable(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s could not be read to tell whether the proxy routes to them: %w", live.RoutingTable, err)
	}
	idle := strings.Fields(said)
	return slices.DeleteFunc(slices.Clone(upstreams), func(upstream string) bool {
		return !slices.Contains(idle, upstream) ||
			slices.ContainsFunc(state.Routes, func(route AppRoute) bool { return route.Upstream == upstream })
	}), nil
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

func cutting(rel Release, standing RoutingTable) cutover {
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

func (c cutover) routed(standing RoutingTable) RoutingTable {
	standing.Grace = c.rel.DrainTimeout
	for _, app := range c.rel.Apps {
		standing.Routes = Routing(standing.Routes, app.route())
	}
	return standing
}

func (c cutover) back(standing RoutingTable) (RoutingTable, error) {
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
	return standing, nil
}

func tellDrain(report providerkit.Reporter, said string) {
	if report == nil {
		return
	}
	for line := range strings.Lines(said) {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 3 && fields[0] == switchboard.DrainExpired:
			report.Detail(fmt.Sprintf("%s still held %s request(s) when the drain window closed: %s",
				fields[1], fields[2], drainCeiling))
		case len(fields) == 2 && fields[0] == switchboard.Drained:
			report.Detail(containerOf(fields[1]) + " reported nothing in flight")
		}
	}
}

type routingPair struct {
	table  []byte
	config []byte
}

func (d routingPair) digest() tableDigest { return tableDigest(contentSum(d.table)) }

type tableDigest string

const (
	routingMoved    = 9
	routingUnseeded = 10
	routingLock     = live.StateRoot
)

func routingLocked(mode string) string {
	return "exec 9<" + quoted(routingLock) + "\nflock " + mode + " 9\n"
}

func pairReading() string {
	held := func(path string) string {
		at := quoted(path)
		return "if [ -f " + at + " ]; then printf '+'; base64 < " + at + " | tr -d '\\n'; fi; printf '\\n'"
	}
	return strings.Join([]string{
		"set -e",
		strings.TrimSuffix(routingLocked("-s"), "\n"),
		held(live.RoutingTable),
		held(ProxyConfig),
	}, "\n")
}

func (h *Host) pairHeld(ctx context.Context) (routingPair, error) {
	said, err := h.reach(ctx, "read "+live.RoutingTable+" and "+ProxyConfig, pairReading(), nil)
	if err != nil {
		return routingPair{}, err
	}
	lines := strings.SplitN(said, "\n", 3)
	if len(lines) < 3 {
		return routingPair{}, providerkit.Refuse(providerkit.CodeNotReady,
			"%s and %s on %s read back as %q, not one line for each", live.RoutingTable, ProxyConfig, h.named(), said)
	}
	var read [2][]byte
	for at, line := range lines[:2] {
		encoded, held := strings.CutPrefix(line, "+")
		if !held {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return routingPair{}, providerkit.Refuse(providerkit.CodeNotReady,
				"%s and %s on %s read back undecodable: %v", live.RoutingTable, ProxyConfig, h.named(), err)
		}
		read[at] = decoded
	}
	return routingPair{table: read[0], config: read[1]}, nil
}

func (h *Host) tableHeld(ctx context.Context) (routingPair, error) {
	held, err := h.pairHeld(ctx)
	if err != nil {
		return routingPair{}, err
	}
	if held.table == nil {
		return routingPair{}, providerkit.Refuse(providerkit.CodeNotReady,
			"%s is missing on %s\nRun `ocel bootstrap` for this box's class", live.RoutingTable, h.named())
	}
	return held, nil
}

const routingRewrites = 5

type composed struct {
	prior     routingPair
	written   tableDigest
	changed   bool
	reloading bool
	is        RoutingTable
}

func (h *Host) composeRouting(ctx context.Context, compose func(RoutingTable) (RoutingTable, error)) (composed, error) {
	rewrites := 0
	for {
		held, err := h.tableHeld(ctx)
		if err != nil {
			return composed{}, err
		}
		standing, err := ReadRoutingTable(held.table)
		if err != nil {
			return composed{}, err
		}
		shaped := composed{prior: held, written: held.digest()}
		next, err := compose(standing)
		if err != nil {
			return shaped, err
		}
		before, err := WriteRoutingTable(standing)
		if err != nil {
			return shaped, err
		}
		after, err := WriteRoutingTable(next)
		if err != nil {
			return shaped, err
		}
		rendered, err := RenderProxyConfig(h.front, standing)
		if err != nil {
			return shaped, err
		}
		if bytes.Equal(before, after) && bytes.Equal(held.config, rendered) {
			return shaped, nil
		}
		admitted, err := RenderProxyConfig(h.front, next)
		if err != nil {
			return shaped, err
		}
		shaped.is = next
		shaped.reloading = !bytes.Equal(held.config, admitted)
		shaped.written, err = h.writePair(ctx, held.digest(), routingPair{table: after, config: admitted})
		shaped.changed = true
		rewrites++
		if err == nil || !moved(err) || rewrites >= routingRewrites {
			return shaped, err
		}
	}
}

func pairFed(pair routingPair) string {
	return base64.StdEncoding.EncodeToString(pair.table) + "\n" + base64.StdEncoding.EncodeToString(pair.config) + "\n"
}

func (h *Host) writePair(ctx context.Context, expected tableDigest, pair routingPair) (tableDigest, error) {
	elevation, refused := h.elevate(ctx)
	result, err := h.stream(ctx, stagedWrite(expected), strings.NewReader(pairFed(pair)), elevation)
	if err != nil {
		return "", err
	}
	switch result.Code {
	case 0:
		return tableDigest(strings.TrimSpace(result.Stdout)), nil
	case routingMoved:
		return "", providerkit.Refuse(providerkit.CodeBusy,
			"%s on %s changed during this deploy (%s, expected %s); nothing was written\nRun the deploy again",
			live.RoutingTable, h.named(), strings.TrimSpace(result.Stderr), expected)
	case routingUnseeded:
		return "", providerkit.Refuse(providerkit.CodeNotReady,
			"%s or %s is missing on %s; nothing was written\nRun `ocel bootstrap` for this box's class",
			live.RoutingTable, ProxyConfig, h.named())
	default:
		return "", unelevated(refused, h.refuse("write "+live.RoutingTable+" and "+ProxyConfig, result))
	}
}

func unelevated(refused, why error) error {
	if refused == nil {
		return why
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"%v\ncould not elevate: %v", why, refused)
}

func stagedWrite(expected tableDigest) string {
	table, config := quoted(live.RoutingTable), quoted(ProxyConfig)
	return strings.Join([]string{
		"set -e",
		`staged=$(mktemp ` + quoted(live.RoutingTable+".XXXXXX") + `)`,
		`rendered=$(mktemp ` + quoted(ProxyConfig+".XXXXXX") + `)`,
		`trap 'rm -f "$staged" "$rendered"' EXIT`,
		`IFS= read -r written`,
		`IFS= read -r rendering`,
		`printf '%s' "$written" | base64 -d > "$staged"`,
		`printf '%s' "$rendering" | base64 -d > "$rendered"`,
		strings.TrimSuffix(routingLocked("-x"), "\n"),
		`if [ ! -f ` + table + ` ] || [ ! -f ` + config + ` ]; then exit ` + strconv.Itoa(routingUnseeded) + `; fi`,
		`held=$(sha256sum ` + table + ` | cut -d' ' -f1)`,
		`if [ "$held" != ` + quoted(string(expected)) + ` ]; then printf '%s' "$held" >&2; exit ` + strconv.Itoa(routingMoved) + `; fi`,
		`chmod --reference=` + table + ` "$staged"`,
		`chown --reference=` + table + ` "$staged"`,
		`chmod --reference=` + config + ` "$rendered"`,
		`chown --reference=` + config + ` "$rendered"`,
		`sha256sum "$staged" | cut -d' ' -f1`,
		`mv "$staged" ` + table,
		`mv "$rendered" ` + config,
		"trap - EXIT",
	}, "\n")
}

func moved(err error) bool {
	var refusal providerkit.Refusal
	return errors.As(err, &refusal) && refusal.Code == providerkit.CodeBusy
}

func gateCommand(window time.Duration, gates []string) []string {
	return switchboardCommand(append([]string{"gate", "--deploy-timeout", seconds(window)}, gates...)...)
}

func idleCommand(targets []string) []string {
	return switchboardCommand(append([]string{"idle"}, targets...)...)
}

func flipCommand(window time.Duration, retiring []string) []string {
	argv := []string{"flip"}
	if len(retiring) > 0 {
		argv = append(argv, "--drain-timeout", seconds(window))
		for _, retiree := range retiring {
			argv = append(argv, "--retire", retiree)
		}
	}
	return switchboardCommand(append(argv, live.RoutingTable)...)
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
		if len(fields) != 2 || fields[0] != switchboard.Ungated {
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
		rel.apps(), h.named(), outcome, verdict, evidence.String(), h.discard(ctx, rel, elevation))}
}

func (h *Host) overtaken(ctx context.Context, rel Release, why error, elevation string) error {
	ctx, stop := sparing(ctx)
	defer stop()
	return Unserved{fmt.Errorf("release %s onto %s: %w; nothing was written, so the box serves what it served before%s",
		rel.apps(), h.named(), why, h.discard(ctx, rel, elevation))}
}

func (h *Host) stranded(ctx context.Context, rel Release, cut cutover, why error, elevation string) error {
	ctx, stop := sparing(ctx)
	defer stop()
	code := providerkit.CodeNotReady
	written := live.RoutingTable + " and " + ProxyConfig
	rolled := written + " untouched"
	if moved(why) {
		code = providerkit.CodeBusy
	} else if restored, err := h.putBack(ctx, cut, elevation); err != nil {
		return providerkit.Refuse(code,
			"release %s onto %s: could not write %s: %v\n%s not restored: %v\n%s left standing",
			rel.apps(), h.named(), written, why, written, err, rel.names())
	} else if restored {
		rolled = written + " restored"
	}
	return Unserved{providerkit.Refuse(code,
		"release %s onto %s: could not write %s; the proxy was not flipped: %v\n%s%s",
		rel.apps(), h.named(), written, why, rolled, h.discard(ctx, rel, elevation))}
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
		rel.apps(), h.named(), outcome, verdict, h.discard(ctx, rel, elevation))}
}

func (h *Host) putBack(ctx context.Context, cut cutover, elevation string) (bool, error) {
	if !cut.composed() {
		return false, nil
	}
	back, err := h.composeRouting(ctx, cut.back)
	if err != nil || !back.changed {
		return false, err
	}
	_, err = h.ran(ctx, "put the proxy back onto the previous release",
		words(switchboardCommand("flip", live.RoutingTable)), nil, elevation)
	return true, err
}

func (h *Host) discard(ctx context.Context, rel Release, elevation string) string {
	targets := make([]string, 0, len(rel.Apps))
	for _, app := range rel.Apps {
		targets = append(targets, app.Target)
	}
	idle, err := h.unheld(ctx, targets, elevation)
	if err != nil {
		return fmt.Sprintf("\n%s left standing: %v", rel.names(), err)
	}
	var left strings.Builder
	for _, app := range rel.Apps {
		if !slices.Contains(idle, app.Target) {
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
