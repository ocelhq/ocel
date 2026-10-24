package release

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

var healthPathCandidates = []string{"/up", "/health", "/healthz", "/"}

const HealthPathExample = `"compute": { "container": { "health": { "path": "/health" } } }`

type HealthPathSearch struct {
	App    string
	Target string
	Window time.Duration
}

type pathAnswer struct {
	path   string
	status int
}

func (a pathAnswer) String() string { return a.path + " answered " + strconv.Itoa(a.status) }

func (a pathAnswer) absent() bool {
	return a.status == http.StatusNotFound || a.status == http.StatusMethodNotAllowed
}

func (c *Containers) FindHealthPath(ctx context.Context, search HealthPathSearch, progress progress.Log) (string, error) {
	elevation, err := ssh.Docker, c.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return "", err
	}
	progress.Say(fmt.Sprintf("Waiting up to %s for %s to answer HTTP, then probing %s in turn: %s sets no %s, so the first that answers anything but 404 or 405 is its health check",
		search.Window, search.App, inWords(healthPathCandidates), search.App, HealthKey))
	found, err := c.box.Stream(ctx, elevation, RenderStartWatch([]string{ContainerOf(search.Target)}, findHealthPathCommand(search)), nil)
	if err != nil {
		return "", c.refuseMissingHealthPath(ctx, search, err.Error(), false, elevation)
	}
	if _, crashed := FindUnstarted(found.Stdout); crashed {
		return "", c.refuseMissingHealthPath(ctx, search, "", true, elevation)
	}
	answers := readPathAnswers(found.Stdout)
	if found.Code == 0 && len(answers) > 0 && !answers[len(answers)-1].absent() {
		path := answers[len(answers)-1].path
		probed := make([]string, 0, len(answers))
		for _, answer := range answers {
			probed = append(probed, answer.path)
		}
		progress.Say(fmt.Sprintf("%s answers its health check on %s (found by probing %s)", search.App, path, strings.Join(probed, ", ")))
		return path, nil
	}
	if len(answers) == len(healthPathCandidates) && answers[len(answers)-1].absent() {
		lines := make([]string, 0, len(answers))
		for _, answer := range answers {
			lines = append(lines, "  "+answer.String())
		}
		return "", refusal.Refuse(refusal.CodeInvalid,
			"find %s's health path on %s: it answered 404 or 405 on every path ocel probes, so none of them is a health check\n%s\nSet %q on %s to the path that answers 2xx when it is healthy, e.g. %s%s",
			search.App, c.box.Named(), strings.Join(lines, "\n"), HealthKey, search.App, HealthPathExample, c.discardSearched(ctx, search, elevation))
	}
	verdict := strings.TrimSpace(found.Stderr)
	if verdict == "" {
		verdict = fmt.Sprintf("the probe exited %d", found.Code)
	}
	return "", c.refuseMissingHealthPath(ctx, search, verdict, false, elevation)
}

func (c *Containers) refuseMissingHealthPath(ctx context.Context, search HealthPathSearch, verdict string, crashed bool, elevation ssh.Acting) error {
	ctx, stop := unwindContext(ctx)
	defer stop()
	name := ContainerOf(search.Target)
	state := c.box.Evidence(ctx, elevation, docker.StateCommand(name))
	if crashed || isUnstarted(state) {
		return refusal.Refuse(refusal.CodeNotReady, "start %s on %s: %s%s",
			search.App, c.box.Named(), c.describeUnstarted(ctx, search.App, name, state, elevation), c.discardSearched(ctx, search, elevation))
	}
	logs := c.readRedactedLogs(ctx, elevation, name, LogCommand(name))
	if logs == "" {
		logs = NoLogOutput
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"find %s's health path on %s: %s\nstate: %s\nlogs (last %s lines): %s\nSet %q on %s to skip probing, e.g. %s%s",
		search.App, c.box.Named(), verdict, state, docker.LogTail, logs, HealthKey, search.App, HealthPathExample, c.discardSearched(ctx, search, elevation))
}

func (c *Containers) discardSearched(ctx context.Context, search HealthPathSearch, elevation ssh.Acting) string {
	ctx, stop := unwindContext(ctx)
	defer stop()
	return c.discard(ctx, Rollout{Apps: []AppRollout{{Target: search.Target}}}, elevation)
}

func readPathAnswers(said string) []pathAnswer {
	var answers []pathAnswer
	for line := range strings.Lines(said) {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != boxapi.Answered {
			continue
		}
		status, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		answers = append(answers, pathAnswer{path: fields[1], status: status})
	}
	return answers
}

func findHealthPathCommand(search HealthPathSearch) []string {
	return routing.SwitchboardCommand(append([]string{"find-health-path", "--deploy-timeout", seconds(search.Window), search.Target}, healthPathCandidates...)...)
}
