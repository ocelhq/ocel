package host

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

var healthPathCandidates = []string{"/up", "/health", "/healthz", "/"}

const healthPathExample = `"health": { "path": "/health" }`

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

func (h *Host) FindHealthPath(ctx context.Context, search HealthPathSearch, progress progress.Log) (string, error) {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return "", err
	}
	say(progress, fmt.Sprintf("Waiting up to %s for %s to answer HTTP, then probing %s in turn: %s sets no %s, so the first that answers anything but 404 or 405 is its health check",
		search.Window, search.App, inWords(healthPathCandidates), search.App, healthKey))
	found, err := h.stream(ctx, watchingStart([]string{containerOf(search.Target)}, findHealthPathCommand(search)), nil, elevation)
	if err != nil {
		return "", h.unfound(ctx, search, err.Error(), false, elevation)
	}
	if _, crashed := unstartedIn(found.Stdout); crashed {
		return "", h.unfound(ctx, search, "", true, elevation)
	}
	answers := readPathAnswers(found.Stdout)
	if found.Code == 0 && len(answers) > 0 && !answers[len(answers)-1].absent() {
		path := answers[len(answers)-1].path
		probed := make([]string, 0, len(answers))
		for _, answer := range answers {
			probed = append(probed, answer.path)
		}
		say(progress, fmt.Sprintf("%s answers its health check on %s (found by probing %s)", search.App, path, strings.Join(probed, ", ")))
		return path, nil
	}
	if len(answers) == len(healthPathCandidates) && answers[len(answers)-1].absent() {
		lines := make([]string, 0, len(answers))
		for _, answer := range answers {
			lines = append(lines, "  "+answer.String())
		}
		return "", refusal.Refuse(refusal.CodeInvalid,
			"find %s's health path on %s: it answered 404 or 405 on every path ocel probes, so none of them is a health check\n%s\nSet %q on %s to the path that answers 2xx when it is healthy, e.g. %s%s",
			search.App, h.named(), strings.Join(lines, "\n"), healthKey, search.App, healthPathExample, h.abandon(ctx, search, elevation))
	}
	verdict := strings.TrimSpace(found.Stderr)
	if verdict == "" {
		verdict = fmt.Sprintf("the probe exited %d", found.Code)
	}
	return "", h.unfound(ctx, search, verdict, false, elevation)
}

func (h *Host) unfound(ctx context.Context, search HealthPathSearch, verdict string, crashed bool, elevation string) error {
	ctx, stop := sparing(ctx)
	defer stop()
	name := containerOf(search.Target)
	state := h.said(ctx, stateCommand(name), elevation)
	if crashed || unstartedState(state) {
		return refusal.Refuse(refusal.CodeNotReady, "start %s on %s: %s%s",
			search.App, h.named(), h.unstarted(ctx, search.App, name, state, elevation), h.abandon(ctx, search, elevation))
	}
	logs := h.said(ctx, logCommand(name), elevation)
	if logs == "" {
		logs = noLogOutput
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"find %s's health path on %s: %s\nstate: %s\nlogs (last %s lines): %s\nSet %q on %s to skip probing, e.g. %s%s",
		search.App, h.named(), verdict, state, appLogTail, logs, healthKey, search.App, healthPathExample, h.abandon(ctx, search, elevation))
}

func (h *Host) abandon(ctx context.Context, search HealthPathSearch, elevation string) string {
	ctx, stop := sparing(ctx)
	defer stop()
	return h.discard(ctx, Release{Apps: []AppRelease{{Target: search.Target}}}, elevation)
}

func readPathAnswers(said string) []pathAnswer {
	var answers []pathAnswer
	for line := range strings.Lines(said) {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != switchboard.Answered {
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
	return switchboardCommand(append([]string{"find-health-path", "--deploy-timeout", seconds(search.Window), search.Target}, healthPathCandidates...)...)
}
