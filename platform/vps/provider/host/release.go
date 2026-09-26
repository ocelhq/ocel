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
	"github.com/ocelhq/ocel/platform/vps/provider/session"
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

func (h *Host) Release(ctx context.Context, rel Release, progress providerkit.Progress) error {
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
	say(progress, "Checking "+strings.Join(gates, ", ")+", then flipping the proxy")
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
	if err := h.takenUp(ctx, shaped, false, elevation); err != nil {
		return h.unfronted(ctx, rel, err, elevation)
	}

	if progress != nil {
		for _, retiree := range cut.retiring {
			progress.Detail(fmt.Sprintf("%s has %s to drain, then %s",
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
	tellDrain(progress, flipped.Stdout)
	return h.settle(ctx, rel, cut, progress, elevation)
}

func (h *Host) settle(ctx context.Context, rel Release, cut cutover, progress providerkit.Progress, elevation string) error {
	ctx, stop := sparing(ctx)
	defer stop()
	var failed, unstopped []string
	idle, err := h.unheld(ctx, cut.retiring, elevation)
	if err != nil {
		for _, retiree := range cut.retiring {
			failed = append(failed, fmt.Sprintf("%s was drained and unrouted but not stopped, so it is still running: %v", containerOf(retiree), err))
		}
	}
	refused := map[string]error{}
	for _, retiree := range idle {
		say(progress, "Stopping "+containerOf(retiree))
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

func tellDrain(progress providerkit.Progress, said string) {
	if progress == nil {
		return
	}
	for line := range strings.Lines(said) {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 3 && fields[0] == switchboard.DrainExpired:
			progress.Detail(fmt.Sprintf("%s still held %s request(s) when the drain window closed: %s",
				fields[1], fields[2], drainCeiling))
		case len(fields) == 2 && fields[0] == switchboard.Drained:
			progress.Detail(containerOf(fields[1]) + " reported nothing in flight")
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
	routingMoved       = 9
	routingUnseeded    = 10
	routingPlaceFailed = 11
	routingLock        = live.StateRoot
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
	restoring   routingPair
	written     tableDigest
	failedPlace error
	changed     bool
	reloading   bool
}

func (h *Host) composeRouting(ctx context.Context, compose func(RoutingTable) (RoutingTable, error)) (composed, error) {
	rewrites := 0
	at := destination(h.front)
	for {
		held, err := h.tableHeld(ctx)
		if err != nil {
			return composed{}, err
		}
		standing, err := ReadRoutingTable(held.table)
		if err != nil {
			return composed{}, err
		}
		shaped := composed{restoring: held, written: held.digest()}
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
		fresh := bytes.Equal(held.config, rendered)
		if at != "" {
			sum, err := h.destinationSum(ctx, at)
			if err != nil {
				return shaped, err
			}
			fresh, shaped.restoring.config = sum == contentSum(rendered), rendered
		}
		if bytes.Equal(before, after) && fresh {
			return shaped, nil
		}
		if bytes.Equal(before, after) && at != "" {
			shaped.reloading = true
			err = h.replace(ctx, held.digest(), at, rendered)
		} else {
			var admitted []byte
			if admitted, err = RenderProxyConfig(h.front, next); err != nil {
				return shaped, err
			}
			shaped.reloading = !bytes.Equal(shaped.restoring.config, admitted) || at != "" && !fresh
			shaped.written, shaped.failedPlace, err = h.writePair(ctx, held.digest(), routingPair{table: after, config: admitted}, shaped.reloading)
			shaped.changed = true
		}
		rewrites++
		if err == nil || !moved(err) || rewrites >= routingRewrites {
			return shaped, err
		}
	}
}

func pairFed(pair routingPair) string {
	return base64.StdEncoding.EncodeToString(pair.table) + "\n" + base64.StdEncoding.EncodeToString(pair.config) + "\n"
}

func (h *Host) writePair(ctx context.Context, expected tableDigest, pair routingPair, placing bool) (tableDigest, error, error) {
	file := h.front.File()
	if file != ProxyConfig && !placing {
		file = ""
	}
	elevation, refused := h.elevate(ctx)
	result, err := h.stream(ctx, stagedWrite(expected, file), strings.NewReader(pairFed(pair)), elevation)
	if err != nil {
		return "", nil, err
	}
	switch result.Code {
	case 0:
		return tableDigest(strings.TrimSpace(result.Stdout)), nil, nil
	case routingPlaceFailed:
		return tableDigest(strings.TrimSpace(result.Stdout)), h.refuse("place "+file+" through the switchboard", result, elevation), nil
	case routingMoved:
		return "", nil, h.movedUnder(expected, result)
	case routingUnseeded:
		seeded := live.RoutingTable
		if file == ProxyConfig {
			seeded += " or " + ProxyConfig
		}
		return "", nil, providerkit.Refuse(providerkit.CodeNotReady,
			"%s is missing on %s; nothing was written\nRun `ocel bootstrap` for this box's class",
			seeded, h.named())
	default:
		return "", nil, unelevated(refused, h.refuse("write "+routingFiles(file), result, elevation))
	}
}

func routingFiles(file string) string {
	if file == "" {
		return live.RoutingTable
	}
	return live.RoutingTable + " and " + file
}

func (h *Host) movedUnder(expected tableDigest, result session.Result) error {
	return providerkit.Refuse(providerkit.CodeBusy,
		"%s on %s changed during this deploy (%s, expected %s); nothing was written\nRun the deploy again",
		live.RoutingTable, h.named(), strings.TrimSpace(result.Stderr), expected)
}

func unelevated(refused, why error) error {
	if refused == nil {
		return why
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"%v\ncould not elevate: %v", why, refused)
}

type stagedFile struct{ at, staged, fed string }

func stagedWrite(expected tableDigest, file string) string {
	files := []stagedFile{{at: live.RoutingTable, staged: "staged", fed: "written"}}
	if file == ProxyConfig {
		files = append(files, stagedFile{at: ProxyConfig, staged: "rendered", fed: "rendering"})
	}
	var staging, taken, decoding, unseeded, owning, moving, placing []string
	for _, file := range files {
		at, staged := quoted(file.at), `"$`+file.staged+`"`
		staging = append(staging, file.staged+`=$(mktemp `+quoted(file.at+".XXXXXX")+`)`)
		taken = append(taken, staged)
		decoding = append(decoding, `printf '%s' "$`+file.fed+`" | base64 -d > `+staged)
		unseeded = append(unseeded, `[ ! -f `+at+` ]`)
		owning = append(owning, `chmod --reference=`+at+` `+staged, `chown --reference=`+at+` `+staged)
		moving = append(moving, `mv `+staged+` `+at)
	}
	if file != "" && file != ProxyConfig {
		placing = append(placing, placeStep(`printf '%s' "$rendering" | base64 -d | `, file))
	}
	return strings.Join(slices.Concat(
		[]string{"set -e"},
		staging,
		[]string{`trap 'rm -f ` + strings.Join(taken, " ") + `' EXIT`, `IFS= read -r written`, `IFS= read -r rendering`},
		decoding,
		[]string{
			strings.TrimSuffix(routingLocked("-x"), "\n"),
			`if ` + strings.Join(unseeded, " || ") + `; then exit ` + strconv.Itoa(routingUnseeded) + `; fi`,
		},
		comparedUnder(expected),
		owning,
		[]string{`sha256sum "$staged" | cut -d' ' -f1`},
		moving,
		placing,
		[]string{"trap - EXIT"},
	), "\n")
}

func comparedUnder(expected tableDigest) []string {
	return []string{
		`held=$(sha256sum ` + quoted(live.RoutingTable) + ` | cut -d' ' -f1)`,
		`if [ "$held" != ` + quoted(string(expected)) + ` ]; then printf '%s' "$held" >&2; exit ` + strconv.Itoa(routingMoved) + `; fi`,
	}
}

func placeStep(feed, at string) string {
	return `if ! ` + feed + words(switchboardFed("place", at)) + `; then exit ` + strconv.Itoa(routingPlaceFailed) + `; fi`
}

func replacement(expected tableDigest, at string) string {
	return strings.Join(slices.Concat(
		[]string{"set -e", strings.TrimSuffix(routingLocked("-x"), "\n")},
		comparedUnder(expected),
		[]string{placeStep("", at)},
	), "\n")
}

func (h *Host) destinationSum(ctx context.Context, at string) (string, error) {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return "", err
	}
	said, err := h.ran(ctx, "read "+at+" through the switchboard", words(switchboardCommand("placed", at)), nil, elevation)
	return strings.TrimSpace(said), err
}

func (h *Host) replace(ctx context.Context, expected tableDigest, at string, rendering []byte) error {
	elevation, refused := h.elevate(ctx)
	result, err := h.stream(ctx, replacement(expected, at), bytes.NewReader(rendering), elevation)
	if err != nil {
		return err
	}
	switch result.Code {
	case 0:
		return nil
	case routingMoved:
		return h.movedUnder(expected, result)
	default:
		return unelevated(refused, h.refuse("place "+at+" through the switchboard", result, elevation))
	}
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
	written := routingFiles(h.front.File())
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

func (h *Host) unfronted(ctx context.Context, rel Release, why error, elevation string) error {
	ctx, stop := sparing(ctx)
	defer stop()
	return Unserved{fmt.Errorf("release %s onto %s: %w; the previous release is still live%s",
		rel.apps(), h.named(), why, h.discard(ctx, rel, elevation))}
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
	return true, errors.Join(back.failedPlace, err)
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
