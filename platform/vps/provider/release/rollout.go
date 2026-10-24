package release

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"

	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	HealthKey    = "compute.container.health.path"
	NoLogOutput  = "(no output)"
	drainCeiling = "open requests get 502 or a truncated response; websockets and server-sent-events reconnect"
)

type Rollout struct {
	Apps          []AppRollout
	DeployTimeout time.Duration
	DrainTimeout  time.Duration
	StillActive   func(ctx context.Context) error
}

type AppRollout struct {
	boxapi.RouteKey
	Target               string
	HealthPath           string
	HealthPathDiscovered bool
	Run                  Run
}

func (a AppRollout) path() string { return "/" + strings.TrimPrefix(a.HealthPath, "/") }

func (a AppRollout) route() boxapi.Route {
	return boxapi.Route{RouteKey: a.RouteKey, Upstream: a.Target}
}

func (a AppRollout) gate() string { return a.Target + a.path() }

func (a AppRollout) name() string { return ContainerOf(a.Target) }

func (a AppRollout) pathSource() string {
	if a.HealthPathDiscovered {
		return fmt.Sprintf("found earlier by probing, since %s sets no %q: set it to name the path yourself, e.g. %s", a.App, HealthKey, HealthPathExample)
	}
	return fmt.Sprintf("set by %q", HealthKey)
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

func ContainerOf(address string) string {
	name, _, _ := strings.Cut(address, ":")
	return name
}

func (c *Containers) RollOut(ctx context.Context, rollout Rollout, progress progress.Log) error {
	for _, app := range rollout.Apps {
		if strings.TrimSpace(app.HealthPath) == "" {
			return router.Unserved{Err: fmt.Errorf("%w%s", refusal.Refuse(refusal.CodeInvalid,
				"release %s onto %s: no health check path\nSet %q in your project configuration",
				app.App, c.box.Named(), HealthKey), c.Discard(ctx, rollout))}
		}
	}
	if len(rollout.Apps) == 0 {
		return nil
	}
	elevation, err := ssh.Docker, c.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return router.Unserved{Err: fmt.Errorf("%w%s", err, c.Discard(ctx, rollout))}
	}

	gates := make([]string, 0, len(rollout.Apps))
	names := make([]string, 0, len(rollout.Apps))
	for _, app := range rollout.Apps {
		gates = append(gates, app.gate())
		names = append(names, app.name())
	}
	progress.Say(rollout.waiting())
	gated, err := c.box.Stream(ctx, elevation, RenderStartWatch(names, gateCommand(rollout.DeployTimeout, gates)), nil)
	if err != nil {
		return c.refuseFailedGate(ctx, rollout, "never came back with an exit code", err.Error(), "", elevation)
	}
	if name, crashed := FindUnstarted(gated.Stdout); crashed {
		return c.refuseUnstartedRollout(ctx, rollout, name, elevation)
	}
	if gated.Code != 0 {
		return c.refuseFailedGate(ctx, rollout, fmt.Sprintf("exited %d", gated.Code), strings.TrimSpace(gated.Stderr), gated.Stdout, elevation)
	}

	var cut cutover
	var overtaken error
	shaped, err := c.routing.Compose(ctx, func(current boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		if rollout.StillActive != nil {
			if overtaken = rollout.StillActive(ctx); overtaken != nil {
				return current, overtaken
			}
		}
		cut = newCutover(rollout, current)
		return cut.routed(current), nil
	})
	if err != nil {
		if overtaken != nil {
			return c.refuseMovedTable(ctx, rollout, overtaken, elevation)
		}
		return c.refuseUnwrittenProxy(ctx, rollout, cut, err, elevation)
	}
	if err := c.routing.TakenUp(ctx, shaped, false, elevation); err != nil {
		return c.refuseUncutover(ctx, rollout, err, elevation)
	}

	for _, retiree := range cut.retiring {
		progress.Say(fmt.Sprintf("Draining retired container %s for up to %s: past that, %s",
			ContainerOf(retiree), rollout.DrainTimeout, drainCeiling))
	}
	cutOver, err := c.box.Stream(ctx, elevation, shell.Words(cutoverCommand(rollout.DrainTimeout, cut.retiring)), nil)
	if err != nil {
		return c.refuseFailedCutover(ctx, rollout, cut, "never came back with an exit code", err.Error(), elevation)
	}
	if cutOver.Code != 0 {
		return c.refuseFailedCutover(ctx, rollout, cut, fmt.Sprintf("exited %d", cutOver.Code), strings.TrimSpace(cutOver.Stderr), elevation)
	}
	tellDrain(progress, cutOver.Stdout, rollout.DrainTimeout)
	return c.stopRetired(ctx, rollout, cut, progress, elevation)
}

func (c *Containers) stopRetired(ctx context.Context, rollout Rollout, cut cutover, progress progress.Log, elevation ssh.Acting) error {
	ctx, stop := unwindContext(ctx)
	defer stop()
	var failed, unstopped []string
	idle, err := c.idleUpstreams(ctx, cut.retiring, elevation)
	if err != nil {
		for _, retiree := range cut.retiring {
			failed = append(failed, fmt.Sprintf("%s was drained and unrouted but not stopped, so it is still running: %v", ContainerOf(retiree), err))
		}
	}
	refused := map[string]error{}
	for _, retiree := range idle {
		progress.Say("Stopping retired container " + ContainerOf(retiree))
		if err := c.Retire(ctx, ContainerOf(retiree)); err != nil {
			unstopped = append(unstopped, retiree)
			refused[retiree] = err
		}
	}
	confirmed, unread := c.stopped(ctx, unstopped, elevation)
	for _, retiree := range unstopped {
		if !slices.Contains(confirmed, retiree) {
			failed = append(failed, fmt.Sprintf("%s was drained and unrouted but not stopped, so it is still running: %v", ContainerOf(retiree), errors.Join(refused[retiree], unread)))
		}
	}
	if len(failed) == 0 {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"release %s onto %s: cut over to %s, which now serve, but what follows the cutover did not all finish:\n%s",
		rollout.apps(), c.box.Named(), rollout.names(), strings.Join(failed, "\n"))
}

func (c *Containers) idleUpstreams(ctx context.Context, upstreams []string, elevation ssh.Acting) ([]string, error) {
	if len(upstreams) == 0 {
		return nil, nil
	}
	said, err := c.box.Run(ctx, elevation, "ask the proxy whether a route or a drain still uses "+listed(upstreams, ContainerOf), shell.Words(idleCommand(upstreams)), nil)
	if err != nil {
		return nil, err
	}
	state, err := c.routing.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s could not be read to tell whether the proxy routes to them: %w", boxapi.RoutingTablePath, err)
	}
	idle := strings.Fields(said)
	return slices.DeleteFunc(slices.Clone(upstreams), func(upstream string) bool {
		return !slices.Contains(idle, upstream) ||
			slices.ContainsFunc(state.Routes, func(route boxapi.Route) bool { return route.Upstream == upstream })
	}), nil
}

func RunningCommand(names []string) string {
	return "for name in " + shell.Words(names) + `; do ` +
		`if said=$(docker inspect --type container --format '{{.State.Running}}' "$name" 2>&1); then printf '%s %s\n' "$name" "$said"; ` +
		`elif printf '%s' "$said" | grep -qi 'no such'; then printf '%s gone\n' "$name"; fi; done`
}

func (c *Containers) stopped(ctx context.Context, upstreams []string, elevation ssh.Acting) ([]string, error) {
	if len(upstreams) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(upstreams))
	for _, upstream := range upstreams {
		names = append(names, ContainerOf(upstream))
	}
	said, err := c.box.Run(ctx, elevation, "read whether "+listed(upstreams, ContainerOf)+" stopped", RunningCommand(names), nil)
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
			if ContainerOf(upstream) == fields[0] {
				gone = append(gone, upstream)
			}
		}
	}
	return gone, nil
}

type cutover struct {
	rollout  Rollout
	prior    []boxapi.Route
	retiring []string
}

func newCutover(rollout Rollout, table boxapi.RoutingTable) cutover {
	cut := cutover{rollout: rollout}
	for _, app := range rollout.Apps {
		at := slices.IndexFunc(table.Routes, func(route boxapi.Route) bool { return route.RouteKey == app.RouteKey })
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

func (c cutover) routed(table boxapi.RoutingTable) boxapi.RoutingTable {
	table.Grace = c.rollout.DrainTimeout
	for _, app := range c.rollout.Apps {
		table.Routes = routing.Route(table.Routes, app.route())
	}
	return table
}

func (c cutover) back(table boxapi.RoutingTable) (boxapi.RoutingTable, error) {
	for _, app := range c.rollout.Apps {
		ours := func(route boxapi.Route) bool { return route.RouteKey == app.RouteKey }
		at := slices.IndexFunc(table.Routes, ours)
		if at < 0 || table.Routes[at].Upstream != app.Target {
			continue
		}
		table.Routes = routing.Unroute(table.Routes, ours)
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
				ContainerOf(fields[1]), requests(fields[2]), window, drainCeiling))
		case len(fields) == 2 && fields[0] == boxapi.Drained:
			progress.Say("Drained retired container " + ContainerOf(fields[1]) + ": nothing was in flight")
		}
	}
}

func requests(count string) string {
	if count == "1" {
		return "1 request"
	}
	return count + " requests"
}

func gateCommand(window time.Duration, gates []string) []string {
	return routing.SwitchboardCommand(append([]string{"gate", "--deploy-timeout", seconds(window)}, gates...)...)
}

func idleCommand(targets []string) []string {
	return routing.SwitchboardCommand(append([]string{"idle"}, targets...)...)
}

func cutoverCommand(window time.Duration, retiring []string) []string {
	argv := []string{"cutover"}
	if len(retiring) > 0 {
		argv = append(argv, "--drain-timeout", seconds(window))
		for _, retiree := range retiring {
			argv = append(argv, "--retire", retiree)
		}
	}
	return routing.SwitchboardCommand(append(argv, boxapi.RoutingTablePath)...)
}

func seconds(window time.Duration) string {
	return strconv.Itoa(int(window.Round(time.Second).Seconds()))
}

const UnwindWindow = 60 * time.Second

func unwindContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), UnwindWindow)
}

func (c *Containers) refuseFailedGate(ctx context.Context, rollout Rollout, outcome, verdict, said string, elevation ssh.Acting) error {
	ctx, stop := unwindContext(ctx)
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
		state := c.box.Evidence(ctx, elevation, docker.StateCommand(app.name()))
		if isUnstarted(state) {
			evidence.WriteString("\n" + c.describeUnstarted(ctx, app.App, app.name(), state, elevation))
			continue
		}
		logs := c.readRedactedLogs(ctx, elevation, app.name(), LogCommand(app.name()))
		if logs == "" {
			logs = NoLogOutput
		}
		fmt.Fprintf(&evidence, "\ngate: http://%s, %s to answer 2xx (%s)\nstate: %s\nlogs (last %s lines): %s",
			app.gate(), rollout.DeployTimeout, app.pathSource(), state, docker.LogTail, logs)
	}
	return router.Unserved{Err: refusal.Refuse(refusal.CodeNotReady,
		"release %s onto %s: the gate %s; the previous release is still live\n%s%s%s",
		rollout.apps(), c.box.Named(), outcome, verdict, evidence.String(), c.discard(ctx, rollout, elevation))}
}

func (c *Containers) refuseUnstartedRollout(ctx context.Context, rollout Rollout, name string, elevation ssh.Acting) error {
	ctx, stop := unwindContext(ctx)
	defer stop()
	app := name
	if at := slices.IndexFunc(rollout.Apps, func(each AppRollout) bool { return each.name() == name }); at >= 0 {
		app = rollout.Apps[at].App
	}
	state := c.box.Evidence(ctx, elevation, docker.StateCommand(name))
	return router.Unserved{Err: refusal.Refuse(refusal.CodeNotReady,
		"release %s onto %s: the previous release is still live\n%s%s",
		rollout.apps(), c.box.Named(), c.describeUnstarted(ctx, app, name, state, elevation), c.discard(ctx, rollout, elevation))}
}

func (c *Containers) refuseMovedTable(ctx context.Context, rollout Rollout, why error, elevation ssh.Acting) error {
	ctx, stop := unwindContext(ctx)
	defer stop()
	return router.Unserved{Err: fmt.Errorf("release %s onto %s: %w; nothing was written, so the box serves what it served before%s",
		rollout.apps(), c.box.Named(), why, c.discard(ctx, rollout, elevation))}
}

func (c *Containers) refuseUnwrittenProxy(ctx context.Context, rollout Rollout, cut cutover, why error, elevation ssh.Acting) error {
	ctx, stop := unwindContext(ctx)
	defer stop()
	code := refusal.CodeNotReady
	written := routing.Files(c.front.File())
	rolled := written + " untouched"
	if routing.Moved(why) {
		code = refusal.CodeBusy
	} else if restored, err := c.restorePrevious(ctx, cut, elevation); err != nil {
		return refusal.Refuse(code,
			"release %s onto %s: could not write %s: %v\n%s not restored: %v\n%s left running",
			rollout.apps(), c.box.Named(), written, why, written, err, rollout.names())
	} else if restored {
		rolled = written + " restored"
	}
	return router.Unserved{Err: refusal.Refuse(code,
		"release %s onto %s: could not write %s; the proxy was not cut over: %v\n%s%s",
		rollout.apps(), c.box.Named(), written, why, rolled, c.discard(ctx, rollout, elevation))}
}

func (c *Containers) refuseUncutover(ctx context.Context, rollout Rollout, why error, elevation ssh.Acting) error {
	ctx, stop := unwindContext(ctx)
	defer stop()
	return router.Unserved{Err: fmt.Errorf("release %s onto %s: %w; the previous release is still live%s",
		rollout.apps(), c.box.Named(), why, c.discard(ctx, rollout, elevation))}
}

func (c *Containers) refuseFailedCutover(ctx context.Context, rollout Rollout, cut cutover, outcome, verdict string, elevation ssh.Acting) error {
	ctx, stop := unwindContext(ctx)
	defer stop()
	if verdict == "" {
		verdict = "no reason given"
	}
	if _, err := c.restorePrevious(ctx, cut, elevation); err != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"release %s onto %s: the cutover helper %s; the live release is unknown\n%s\nproxy not restored; %s may be live and were left running: %v",
			rollout.apps(), c.box.Named(), outcome, verdict, rollout.names(), err)
	}
	return router.Unserved{Err: refusal.Refuse(refusal.CodeNotReady,
		"release %s onto %s: the cutover helper %s; the previous release is still live\n%s%s",
		rollout.apps(), c.box.Named(), outcome, verdict, c.discard(ctx, rollout, elevation))}
}

func (c *Containers) restorePrevious(ctx context.Context, cut cutover, elevation ssh.Acting) (bool, error) {
	if !cut.composed() {
		return false, nil
	}
	back, err := c.routing.Compose(ctx, cut.back)
	if err != nil || !back.Changed {
		return false, err
	}
	_, err = c.box.Run(ctx, elevation, "put the proxy back onto the previous release", shell.Words(routing.SwitchboardCommand("cutover", boxapi.RoutingTablePath)), nil)
	return true, errors.Join(back.FailedPlace, err)
}

func (c *Containers) Discard(ctx context.Context, rollout Rollout) string {
	ctx, stop := unwindContext(ctx)
	defer stop()
	elevation, err := ssh.Docker, c.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return fmt.Sprintf("\n%s left running: %v", rollout.names(), err)
	}
	return c.discard(ctx, rollout, elevation)
}

func (c *Containers) discard(ctx context.Context, rollout Rollout, elevation ssh.Acting) string {
	targets := make([]string, 0, len(rollout.Apps))
	for _, app := range rollout.Apps {
		targets = append(targets, app.Target)
	}
	idle, err := c.idleUpstreams(ctx, targets, elevation)
	if err != nil {
		return fmt.Sprintf("\n%s left running: %v", rollout.names(), err)
	}
	var left strings.Builder
	for _, app := range rollout.Apps {
		if !slices.Contains(idle, app.Target) {
			continue
		}
		if app.Run == RunRestarted {
			if err := c.Retire(ctx, app.name()); err != nil {
				fmt.Fprintf(&left, "\n%s left running: %v", app.name(), err)
				continue
			}
			fmt.Fprintf(&left, "\n%s stopped", app.name())
			continue
		}
		if err := c.Remove(ctx, app.name()); err != nil {
			fmt.Fprintf(&left, "\n%s left running: %v", app.name(), err)
			continue
		}
		fmt.Fprintf(&left, "\n%s removed", app.name())
	}
	return left.String()
}
