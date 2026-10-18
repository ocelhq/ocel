package host

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"

	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	healthKey    = "compute.container.health.path"
	appLogTail   = "200"
	noLogOutput  = "(no output)"
	drainCeiling = "open requests get 502 or a truncated response; websockets and server-sent-events reconnect"
)

type Rollout struct {
	Apps          []AppRollout
	DeployTimeout time.Duration
	DrainTimeout  time.Duration
	StillActive   func(ctx context.Context) error
}

type AppRollout struct {
	RouteKey
	Target               string
	HealthPath           string
	HealthPathDiscovered bool
	Run                  Run
}

func (a AppRollout) path() string { return "/" + strings.TrimPrefix(a.HealthPath, "/") }

func (a AppRollout) route() AppRoute {
	return AppRoute{RouteKey: a.RouteKey, Upstream: a.Target}
}

func (a AppRollout) gate() string { return a.Target + a.path() }

func (a AppRollout) name() string { return containerOf(a.Target) }

func (a AppRollout) pathSource() string {
	if a.HealthPathDiscovered {
		return fmt.Sprintf("found earlier by probing, since %s sets no %q: set it to name the path yourself, e.g. %s", a.App, healthKey, healthPathExample)
	}
	return fmt.Sprintf("set by %q", healthKey)
}

func (r Rollout) apps() string { return listed(r.Apps, func(app AppRollout) string { return app.App }) }

func (r Rollout) names() string { return listed(r.Apps, AppRollout.name) }

func (r Rollout) waiting() string {
	them := "it"
	if len(r.Apps) > 1 {
		them = "them"
	}
	apps := make([]string, 0, len(r.Apps))
	gates := make([]string, 0, len(r.Apps))
	for _, app := range r.Apps {
		apps, gates = append(apps, app.App), append(gates, app.gate())
	}
	return fmt.Sprintf("Waiting up to %s for %s to answer 2xx on %s, then switching the proxy to %s",
		r.DeployTimeout, inWords(apps), inWords(gates), them)
}

func inWords(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

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

func (h *Host) RollOut(ctx context.Context, rollout Rollout, progress progress.Log) error {
	for _, app := range rollout.Apps {
		if strings.TrimSpace(app.HealthPath) == "" {
			return router.Unserved{Err: fmt.Errorf("%w%s", refusal.Refuse(refusal.CodeInvalid,
				"release %s onto %s: no health check path\nSet %q in your project configuration",
				app.App, h.Named(), healthKey), h.Discard(ctx, rollout))}
		}
	}
	if len(rollout.Apps) == 0 {
		return nil
	}
	elevation, err := ssh.Docker, h.Assume(ctx, ssh.Docker)
	if err != nil {
		return router.Unserved{Err: fmt.Errorf("%w%s", err, h.Discard(ctx, rollout))}
	}

	gates := make([]string, 0, len(rollout.Apps))
	names := make([]string, 0, len(rollout.Apps))
	for _, app := range rollout.Apps {
		gates = append(gates, app.gate())
		names = append(names, app.name())
	}
	progress.Say(rollout.waiting())
	gated, err := h.Stream(ctx, elevation, renderStartWatch(names, gateCommand(rollout.DeployTimeout, gates)), nil)
	if err != nil {
		return h.ungated(ctx, rollout, "never came back with an exit code", err.Error(), "", elevation)
	}
	if name, crashed := findUnstarted(gated.Stdout); crashed {
		return h.refuseUnstartedRollout(ctx, rollout, name, elevation)
	}
	if gated.Code != 0 {
		return h.ungated(ctx, rollout, fmt.Sprintf("exited %d", gated.Code), strings.TrimSpace(gated.Stderr), gated.Stdout, elevation)
	}

	var cut cutover
	var overtaken error
	shaped, err := h.composeRouting(ctx, func(current RoutingTable) (RoutingTable, error) {
		if rollout.StillActive != nil {
			if overtaken = rollout.StillActive(ctx); overtaken != nil {
				return current, overtaken
			}
		}
		cut = cutting(rollout, current)
		return cut.routed(current), nil
	})
	if err != nil {
		if overtaken != nil {
			return h.overtaken(ctx, rollout, overtaken, elevation)
		}
		return h.stranded(ctx, rollout, cut, err, elevation)
	}
	if err := h.takenUp(ctx, shaped, false, elevation); err != nil {
		return h.unfronted(ctx, rollout, err, elevation)
	}

	for _, retiree := range cut.retiring {
		progress.Say(fmt.Sprintf("Draining retired container %s for up to %s: past that, %s",
			containerOf(retiree), rollout.DrainTimeout, drainCeiling))
	}
	cutOver, err := h.Stream(ctx, elevation, shell.Words(cutoverCommand(rollout.DrainTimeout, cut.retiring)), nil)
	if err != nil {
		return h.refuseFailedCutover(ctx, rollout, cut, "never came back with an exit code", err.Error(), elevation)
	}
	if cutOver.Code != 0 {
		return h.refuseFailedCutover(ctx, rollout, cut, fmt.Sprintf("exited %d", cutOver.Code), strings.TrimSpace(cutOver.Stderr), elevation)
	}
	tellDrain(progress, cutOver.Stdout, rollout.DrainTimeout)
	return h.stopRetired(ctx, rollout, cut, progress, elevation)
}

func (h *Host) stopRetired(ctx context.Context, rollout Rollout, cut cutover, progress progress.Log, elevation ssh.Acting) error {
	ctx, stop := sparing(ctx)
	defer stop()
	var failed, unstopped []string
	idle, err := h.idleUpstreams(ctx, cut.retiring, elevation)
	if err != nil {
		for _, retiree := range cut.retiring {
			failed = append(failed, fmt.Sprintf("%s was drained and unrouted but not stopped, so it is still running: %v", containerOf(retiree), err))
		}
	}
	refused := map[string]error{}
	for _, retiree := range idle {
		progress.Say("Stopping retired container " + containerOf(retiree))
		if err := h.RetireContainer(ctx, containerOf(retiree)); err != nil {
			unstopped = append(unstopped, retiree)
			refused[retiree] = err
		}
	}
	confirmed, unread := h.stopped(ctx, unstopped, elevation)
	for _, retiree := range unstopped {
		if !slices.Contains(confirmed, retiree) {
			failed = append(failed, fmt.Sprintf("%s was drained and unrouted but not stopped, so it is still running: %v", containerOf(retiree), errors.Join(refused[retiree], unread)))
		}
	}
	if len(failed) == 0 {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"release %s onto %s: cut over to %s, which now serve, but what follows the cutover did not all finish:\n%s",
		rollout.apps(), h.Named(), rollout.names(), strings.Join(failed, "\n"))
}

func (h *Host) idleUpstreams(ctx context.Context, upstreams []string, elevation ssh.Acting) ([]string, error) {
	if len(upstreams) == 0 {
		return nil, nil
	}
	said, err := h.Run(ctx, elevation, "ask the proxy whether a route or a drain still uses "+listed(upstreams, containerOf), shell.Words(idleCommand(upstreams)), nil)
	if err != nil {
		return nil, err
	}
	state, err := h.routingTable(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s could not be read to tell whether the proxy routes to them: %w", boxapi.RoutingTable, err)
	}
	idle := strings.Fields(said)
	return slices.DeleteFunc(slices.Clone(upstreams), func(upstream string) bool {
		return !slices.Contains(idle, upstream) ||
			slices.ContainsFunc(state.Routes, func(route AppRoute) bool { return route.Upstream == upstream })
	}), nil
}

func runningCommand(names []string) string {
	return "for name in " + shell.Words(names) + `; do ` +
		`if said=$(docker inspect --type container --format '{{.State.Running}}' "$name" 2>&1); then printf '%s %s\n' "$name" "$said"; ` +
		`elif printf '%s' "$said" | grep -qi 'no such'; then printf '%s gone\n' "$name"; fi; done`
}

func (h *Host) stopped(ctx context.Context, upstreams []string, elevation ssh.Acting) ([]string, error) {
	if len(upstreams) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(upstreams))
	for _, upstream := range upstreams {
		names = append(names, containerOf(upstream))
	}
	said, err := h.Run(ctx, elevation, "read whether "+listed(upstreams, containerOf)+" stopped", runningCommand(names), nil)
	if err != nil {
		return nil, err
	}
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
	return gone, nil
}

type cutover struct {
	rollout  Rollout
	prior    []AppRoute
	retiring []string
}

func cutting(rollout Rollout, table RoutingTable) cutover {
	cut := cutover{rollout: rollout}
	for _, app := range rollout.Apps {
		at := slices.IndexFunc(table.Routes, func(route AppRoute) bool { return route.RouteKey == app.RouteKey })
		if at < 0 {
			continue
		}
		cut.prior = append(cut.prior, table.Routes[at])
		if upstream := table.Routes[at].Upstream; upstream != app.Target {
			cut.retiring = append(cut.retiring, upstream)
		}
	}
	slices.Sort(cut.retiring)
	cut.retiring = slices.Compact(cut.retiring)
	return cut
}

func (c cutover) composed() bool { return len(c.rollout.Apps) > 0 }

func (c cutover) routed(table RoutingTable) RoutingTable {
	table.Grace = c.rollout.DrainTimeout
	for _, app := range c.rollout.Apps {
		table.Routes = Routing(table.Routes, app.route())
	}
	return table
}

func (c cutover) back(table RoutingTable) (RoutingTable, error) {
	for _, app := range c.rollout.Apps {
		ours := func(route AppRoute) bool { return route.RouteKey == app.RouteKey }
		at := slices.IndexFunc(table.Routes, ours)
		if at < 0 || table.Routes[at].Upstream != app.Target {
			continue
		}
		table.Routes = Unrouting(table.Routes, ours)
		if was := slices.IndexFunc(c.prior, ours); was >= 0 {
			table.Routes = append(table.Routes, c.prior[was])
		}
	}
	return table, nil
}

func tellDrain(progress progress.Log, said string, window time.Duration) {
	for line := range strings.Lines(said) {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 3 && fields[0] == boxapi.DrainExpired:
			progress.Warn(fmt.Sprintf("Retired container %s still had %s in flight when its %s drain window closed: %s",
				containerOf(fields[1]), requests(fields[2]), window, drainCeiling))
		case len(fields) == 2 && fields[0] == boxapi.Drained:
			progress.Say("Drained retired container " + containerOf(fields[1]) + ": nothing was in flight")
		}
	}
}

func requests(count string) string {
	if count == "1" {
		return "1 request"
	}
	return count + " requests"
}

type routingPair struct {
	table   []byte
	config  []byte
	origins []proxy.OriginFile
}

func (d routingPair) digest() tableDigest { return tableDigest(contentSum(d.table)) }

type tableDigest string

const (
	routingMoved       = 9
	routingUnseeded    = 10
	routingPlaceFailed = 11
	routingLock        = boxapi.StateRoot
)

func routingLocked(mode string) string {
	return shell.Locked(routingLock, mode, shell.RoutingLockSeconds)
}

func pairReading() string {
	printed := func(path string) string {
		at := shell.Quote(path)
		return "if [ -f " + at + " ]; then printf '+'; base64 < " + at + " | tr -d '\\n'; fi; printf '\\n'"
	}
	return strings.Join([]string{
		"set -e",
		strings.TrimSuffix(routingLocked("-s"), "\n"),
		printed(boxapi.RoutingTable),
		printed(ProxyConfig),
	}, "\n")
}

func (h *Host) currentPair(ctx context.Context) (routingPair, error) {
	said, err := h.Run(ctx, ssh.RootWhenAble, "read "+boxapi.RoutingTable+" and "+ProxyConfig, pairReading(), nil)
	if err != nil {
		return routingPair{}, err
	}
	lines := strings.SplitN(said, "\n", 3)
	if len(lines) < 3 {
		return routingPair{}, refusal.Refuse(refusal.CodeNotReady,
			"%s and %s on %s read back as %d lines (%d bytes), not one line for each", boxapi.RoutingTable, ProxyConfig, h.Named(), strings.Count(said, "\n"), len(said))
	}
	var read [2][]byte
	for at, line := range lines[:2] {
		encoded, present := strings.CutPrefix(line, "+")
		if !present {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return routingPair{}, refusal.Refuse(refusal.CodeNotReady,
				"%s and %s on %s read back undecodable: %v", boxapi.RoutingTable, ProxyConfig, h.Named(), err)
		}
		read[at] = decoded
	}
	return routingPair{table: read[0], config: read[1]}, nil
}

func (h *Host) currentTable(ctx context.Context) (routingPair, error) {
	pair, err := h.currentPair(ctx)
	if err != nil {
		return routingPair{}, err
	}
	if pair.table == nil {
		return routingPair{}, refusal.Refuse(refusal.CodeNotReady,
			"%s is missing on %s\nRun `ocel bootstrap` for this box's tier", boxapi.RoutingTable, h.Named())
	}
	return pair, nil
}

const routingRewrites = 5

type composed struct {
	restoring   routingPair
	written     tableDigest
	failedPlace error
	changed     bool
	reloading   bool
	served      proxy.Spec
	admitted    proxy.Spec
}

func (h *Host) composeRouting(ctx context.Context, compose func(RoutingTable) (RoutingTable, error)) (composed, error) {
	rewrites := 0
	at := destination(h.front)
	for {
		pair, err := h.currentTable(ctx)
		if err != nil {
			return composed{}, err
		}
		table, err := ReadRoutingTable(pair.table)
		if err != nil {
			return composed{}, err
		}
		shaped := composed{restoring: pair, written: pair.digest()}
		next, err := compose(table)
		if err != nil {
			return shaped, err
		}
		if added := slices.DeleteFunc(next.hostnames(), func(hostname string) bool {
			return slices.Contains(table.hostnames(), hostname)
		}); len(added) > 0 {
			if err := h.front.RefuseRouted(ctx, added); err != nil {
				return shaped, err
			}
		}
		before, err := WriteRoutingTable(table)
		if err != nil {
			return shaped, err
		}
		after, err := WriteRoutingTable(next)
		if err != nil {
			return shaped, err
		}
		rendered, err := RenderProxyConfig(h.front, table)
		if err != nil {
			return shaped, err
		}
		fresh := bytes.Equal(pair.config, rendered)
		if at != "" {
			sum, err := h.destinationSum(ctx, at)
			if err != nil {
				return shaped, err
			}
			fresh, shaped.restoring.config = sum == contentSum(rendered), rendered
			if shaped.restoring.origins, err = RenderOriginFiles(h.front, table); err != nil {
				return shaped, err
			}
		}
		if bytes.Equal(before, after) && fresh {
			return shaped, nil
		}
		if bytes.Equal(before, after) && at != "" {
			if err := h.front.Validate(ctx, rendered); err != nil {
				return shaped, err
			}
			shaped.reloading, shaped.admitted = true, proxySpec(table)
			err = h.replace(ctx, pair.digest(), at, rendered, shaped.restoring.origins)
		} else {
			var admitted []byte
			if admitted, err = RenderProxyConfig(h.front, next); err != nil {
				return shaped, err
			}
			var origins []proxy.OriginFile
			if origins, err = RenderOriginFiles(h.front, next); err != nil {
				return shaped, err
			}
			shaped.reloading = !bytes.Equal(shaped.restoring.config, admitted) || at != "" && !fresh
			if at != "" && shaped.reloading {
				if err := h.front.Validate(ctx, admitted); err != nil {
					return shaped, err
				}
			}
			if shaped.admitted = proxySpec(next); fresh {
				shaped.served = proxySpec(table)
			}
			shaped.written, shaped.failedPlace, err = h.writePair(ctx, pair.digest(), routingPair{table: after, config: admitted, origins: origins}, shaped.reloading)
			shaped.changed = true
		}
		rewrites++
		if err == nil || !moved(err) || rewrites >= routingRewrites {
			return shaped, err
		}
	}
}

func pairFed(pair routingPair) string {
	return base64.StdEncoding.EncodeToString(pair.table) + "\n" + base64.StdEncoding.EncodeToString(pair.config) + "\n" + originsFed(pair.origins)
}

func originsFed(origins []proxy.OriginFile) string {
	var fed strings.Builder
	for _, origin := range origins {
		fed.WriteString(base64.StdEncoding.EncodeToString(origin.Bundle) + "\n")
	}
	return fed.String()
}

func originPaths(origins []proxy.OriginFile) []string {
	paths := make([]string, 0, len(origins))
	for _, origin := range origins {
		paths = append(paths, origin.Path)
	}
	return paths
}

func originReading(paths []string) []string {
	reads := make([]string, 0, len(paths))
	for at := range paths {
		reads = append(reads, `IFS= read -r origin`+strconv.Itoa(at))
	}
	return reads
}

func originPlacing(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	dir := filepath.Dir(paths[0])
	steps := []string{`if ! group=$(` + shell.Words(placementCommand(dir, "origin-group")) + ` </dev/null); then exit ` + strconv.Itoa(routingPlaceFailed) + `; fi`}
	for at, path := range paths {
		steps = append(steps, `if ! printf '%s' "$origin`+strconv.Itoa(at)+`" | base64 -d | docker run -i --user "0:$group" `+
			shell.Words(placementCommand(dir, "place-origin", path)[2:])+`; then exit `+strconv.Itoa(routingPlaceFailed)+`; fi`)
	}
	return steps
}

func originUnplacing(dir string, kept []string) string {
	return `if ! ` + shell.Words(placementCommand(dir, append([]string{"unplace-origins"}, kept...)...)) +
		` </dev/null; then exit ` + strconv.Itoa(routingPlaceFailed) + `; fi`
}

func (h *Host) writePair(ctx context.Context, expected tableDigest, pair routingPair, placing bool) (tableDigest, error, error) {
	file := h.front.File()
	if file != ProxyConfig && !placing {
		file = ""
	}
	elevation := ssh.RootWhenAble
	result, err := h.Stream(ctx, elevation, stagedWrite(expected, file, originPaths(pair.origins)), strings.NewReader(pairFed(pair)))
	if err != nil {
		return "", nil, err
	}
	switch result.Code {
	case 0:
		return tableDigest(strings.TrimSpace(result.Stdout)), nil, nil
	case routingPlaceFailed:
		return tableDigest(strings.TrimSpace(result.Stdout)), h.Refuse("place "+file, result), nil
	case routingMoved:
		return "", nil, h.movedUnder(expected, result)
	case routingUnseeded:
		seeded := boxapi.RoutingTable
		if file == ProxyConfig {
			seeded += " or " + ProxyConfig
		}
		return "", nil, refusal.Refuse(refusal.CodeNotReady,
			"%s is missing on %s; nothing was written\nRun `ocel bootstrap` for this box's tier",
			seeded, h.Named())
	default:
		return "", nil, h.Refuse("write "+routingFiles(file), result)
	}
}

func routingFiles(file string) string {
	if file == "" {
		return boxapi.RoutingTable
	}
	return boxapi.RoutingTable + " and " + file
}

func (h *Host) movedUnder(expected tableDigest, result ssh.Result) error {
	return refusal.Refuse(refusal.CodeBusy,
		"%s on %s changed during this deploy (%s, expected %s); nothing was written\nRun the deploy again",
		boxapi.RoutingTable, h.Named(), strings.TrimSpace(result.Stderr), expected)
}

type stagedFile struct{ at, staged, fed string }

func stagedWrite(expected tableDigest, file string, origins []string) string {
	files := []stagedFile{{at: boxapi.RoutingTable, staged: "staged", fed: "written"}}
	if file == ProxyConfig {
		files = append(files, stagedFile{at: ProxyConfig, staged: "rendered", fed: "rendering"})
	}
	var staging, taken, decoding, unseeded, owning, moving, reading, placing []string
	for _, file := range files {
		at, staged := shell.Quote(file.at), `"$`+file.staged+`"`
		staging = append(staging, file.staged+`=$(mktemp `+shell.Quote(file.at+".XXXXXX")+`)`)
		taken = append(taken, staged)
		decoding = append(decoding, `printf '%s' "$`+file.fed+`" | base64 -d > `+staged)
		unseeded = append(unseeded, `[ ! -f `+at+` ]`)
		owning = append(owning, `chmod --reference=`+at+` `+staged, `chown --reference=`+at+` `+staged)
		moving = append(moving, `mv `+staged+` `+at)
	}
	if file != "" && file != ProxyConfig {
		reading = originReading(origins)
		placing = slices.Concat(originPlacing(origins), []string{placeStep(`printf '%s' "$rendering" | base64 -d | `, file), originUnplacing(filepath.Dir(file), origins)})
	}
	return strings.Join(slices.Concat(
		[]string{"set -e"},
		staging,
		[]string{`trap 'rm -f ` + strings.Join(taken, " ") + `' EXIT`, `IFS= read -r written`, `IFS= read -r rendering`},
		reading,
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
		`current=$(sha256sum ` + shell.Quote(boxapi.RoutingTable) + ` | cut -d' ' -f1)`,
		`if [ "$current" != ` + shell.Quote(string(expected)) + ` ]; then printf '%s' "$current" >&2; exit ` + strconv.Itoa(routingMoved) + `; fi`,
	}
}

func placeStep(feed, at string) string {
	return `if ! ` + feed + shell.Words(placementFed(filepath.Dir(at), "place", at)) + `; then exit ` + strconv.Itoa(routingPlaceFailed) + `; fi`
}

func replacement(expected tableDigest, at string, origins []string) string {
	return strings.Join(slices.Concat(
		[]string{"set -e"},
		originReading(origins),
		[]string{strings.TrimSuffix(routingLocked("-x"), "\n")},
		comparedUnder(expected),
		originPlacing(origins),
		[]string{placeStep("", at), originUnplacing(filepath.Dir(at), origins)},
	), "\n")
}

func (h *Host) destinationSum(ctx context.Context, at string) (string, error) {
	elevation, err := ssh.Docker, h.Assume(ctx, ssh.Docker)
	if err != nil {
		return "", err
	}
	said, err := h.Run(ctx, elevation, "read "+at, shell.Words(placementCommand(filepath.Dir(at), "placed", at)), nil)
	return strings.TrimSpace(said), err
}

func (h *Host) replace(ctx context.Context, expected tableDigest, at string, rendering []byte, origins []proxy.OriginFile) error {
	elevation := ssh.RootWhenAble
	result, err := h.Stream(ctx, elevation, replacement(expected, at, originPaths(origins)), strings.NewReader(originsFed(origins)+string(rendering)))
	if err != nil {
		return err
	}
	switch result.Code {
	case 0:
		return nil
	case routingMoved:
		return h.movedUnder(expected, result)
	default:
		return h.Refuse("place "+at, result)
	}
}

func moved(err error) bool {
	var refused refusal.Refusal
	return errors.As(err, &refused) && refused.Code == refusal.CodeBusy
}

func gateCommand(window time.Duration, gates []string) []string {
	return switchboardCommand(append([]string{"gate", "--deploy-timeout", seconds(window)}, gates...)...)
}

func idleCommand(targets []string) []string {
	return switchboardCommand(append([]string{"idle"}, targets...)...)
}

func cutoverCommand(window time.Duration, retiring []string) []string {
	argv := []string{"cutover"}
	if len(retiring) > 0 {
		argv = append(argv, "--drain-timeout", seconds(window))
		for _, retiree := range retiring {
			argv = append(argv, "--retire", retiree)
		}
	}
	return switchboardCommand(append(argv, boxapi.RoutingTable)...)
}

func seconds(window time.Duration) string {
	return strconv.Itoa(int(window.Round(time.Second).Seconds()))
}

const UnwindWindow = 60 * time.Second

func sparing(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), UnwindWindow)
}

func (h *Host) ungated(ctx context.Context, rollout Rollout, outcome, verdict, said string, elevation ssh.Acting) error {
	ctx, stop := sparing(ctx)
	defer stop()
	if verdict == "" {
		verdict = "no reason given"
	}
	failed := rollout.Apps
	for line := range strings.Lines(said) {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != boxapi.Ungated {
			continue
		}
		if at := slices.IndexFunc(rollout.Apps, func(app AppRollout) bool { return app.gate() == fields[1] }); at >= 0 {
			failed = rollout.Apps[at : at+1]
		}
	}
	var evidence strings.Builder
	for _, app := range failed {
		state := h.Evidence(ctx, elevation, stateCommand(app.name()))
		if isUnstarted(state) {
			evidence.WriteString("\n" + h.describeUnstarted(ctx, app.App, app.name(), state, elevation))
			continue
		}
		logs := h.Evidence(ctx, elevation, logCommand(app.name()))
		if logs == "" {
			logs = noLogOutput
		}
		fmt.Fprintf(&evidence, "\ngate: http://%s, %s to answer 2xx (%s)\nstate: %s\nlogs (last %s lines): %s",
			app.gate(), rollout.DeployTimeout, app.pathSource(), state, appLogTail, logs)
	}
	return router.Unserved{Err: refusal.Refuse(refusal.CodeNotReady,
		"release %s onto %s: the gate %s; the previous release is still live\n%s%s%s",
		rollout.apps(), h.Named(), outcome, verdict, evidence.String(), h.discard(ctx, rollout, elevation))}
}

func (h *Host) refuseUnstartedRollout(ctx context.Context, rollout Rollout, name string, elevation ssh.Acting) error {
	ctx, stop := sparing(ctx)
	defer stop()
	app := name
	if at := slices.IndexFunc(rollout.Apps, func(each AppRollout) bool { return each.name() == name }); at >= 0 {
		app = rollout.Apps[at].App
	}
	state := h.Evidence(ctx, elevation, stateCommand(name))
	return router.Unserved{Err: refusal.Refuse(refusal.CodeNotReady,
		"release %s onto %s: the previous release is still live\n%s%s",
		rollout.apps(), h.Named(), h.describeUnstarted(ctx, app, name, state, elevation), h.discard(ctx, rollout, elevation))}
}

func (h *Host) overtaken(ctx context.Context, rollout Rollout, why error, elevation ssh.Acting) error {
	ctx, stop := sparing(ctx)
	defer stop()
	return router.Unserved{Err: fmt.Errorf("release %s onto %s: %w; nothing was written, so the box serves what it served before%s",
		rollout.apps(), h.Named(), why, h.discard(ctx, rollout, elevation))}
}

func (h *Host) stranded(ctx context.Context, rollout Rollout, cut cutover, why error, elevation ssh.Acting) error {
	ctx, stop := sparing(ctx)
	defer stop()
	code := refusal.CodeNotReady
	written := routingFiles(h.front.File())
	rolled := written + " untouched"
	if moved(why) {
		code = refusal.CodeBusy
	} else if restored, err := h.putBack(ctx, cut, elevation); err != nil {
		return refusal.Refuse(code,
			"release %s onto %s: could not write %s: %v\n%s not restored: %v\n%s left running",
			rollout.apps(), h.Named(), written, why, written, err, rollout.names())
	} else if restored {
		rolled = written + " restored"
	}
	return router.Unserved{Err: refusal.Refuse(code,
		"release %s onto %s: could not write %s; the proxy was not cut over: %v\n%s%s",
		rollout.apps(), h.Named(), written, why, rolled, h.discard(ctx, rollout, elevation))}
}

func (h *Host) unfronted(ctx context.Context, rollout Rollout, why error, elevation ssh.Acting) error {
	ctx, stop := sparing(ctx)
	defer stop()
	return router.Unserved{Err: fmt.Errorf("release %s onto %s: %w; the previous release is still live%s",
		rollout.apps(), h.Named(), why, h.discard(ctx, rollout, elevation))}
}

func (h *Host) refuseFailedCutover(ctx context.Context, rollout Rollout, cut cutover, outcome, verdict string, elevation ssh.Acting) error {
	ctx, stop := sparing(ctx)
	defer stop()
	if verdict == "" {
		verdict = "no reason given"
	}
	if _, err := h.putBack(ctx, cut, elevation); err != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"release %s onto %s: the cutover helper %s; the live release is unknown\n%s\nproxy not restored; %s may be live and were left running: %v",
			rollout.apps(), h.Named(), outcome, verdict, rollout.names(), err)
	}
	return router.Unserved{Err: refusal.Refuse(refusal.CodeNotReady,
		"release %s onto %s: the cutover helper %s; the previous release is still live\n%s%s",
		rollout.apps(), h.Named(), outcome, verdict, h.discard(ctx, rollout, elevation))}
}

func (h *Host) putBack(ctx context.Context, cut cutover, elevation ssh.Acting) (bool, error) {
	if !cut.composed() {
		return false, nil
	}
	back, err := h.composeRouting(ctx, cut.back)
	if err != nil || !back.changed {
		return false, err
	}
	_, err = h.Run(ctx, elevation, "put the proxy back onto the previous release", shell.Words(switchboardCommand("cutover", boxapi.RoutingTable)), nil)
	return true, errors.Join(back.failedPlace, err)
}

func (h *Host) Discard(ctx context.Context, rollout Rollout) string {
	ctx, stop := sparing(ctx)
	defer stop()
	elevation, err := ssh.Docker, h.Assume(ctx, ssh.Docker)
	if err != nil {
		return fmt.Sprintf("\n%s left running: %v", rollout.names(), err)
	}
	return h.discard(ctx, rollout, elevation)
}

func (h *Host) discard(ctx context.Context, rollout Rollout, elevation ssh.Acting) string {
	targets := make([]string, 0, len(rollout.Apps))
	for _, app := range rollout.Apps {
		targets = append(targets, app.Target)
	}
	idle, err := h.idleUpstreams(ctx, targets, elevation)
	if err != nil {
		return fmt.Sprintf("\n%s left running: %v", rollout.names(), err)
	}
	var left strings.Builder
	for _, app := range rollout.Apps {
		if !slices.Contains(idle, app.Target) {
			continue
		}
		if app.Run == RunRestarted {
			if err := h.RetireContainer(ctx, app.name()); err != nil {
				fmt.Fprintf(&left, "\n%s left running: %v", app.name(), err)
				continue
			}
			fmt.Fprintf(&left, "\n%s stopped", app.name())
			continue
		}
		if err := h.RemoveContainer(ctx, app.name()); err != nil {
			fmt.Fprintf(&left, "\n%s left running: %v", app.name(), err)
			continue
		}
		fmt.Fprintf(&left, "\n%s removed", app.name())
	}
	return left.String()
}
