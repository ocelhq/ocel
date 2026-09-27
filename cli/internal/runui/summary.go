package runui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fatih/color"

	"github.com/ocelhq/ocel/cli/internal/envgate"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const (
	blockIndent  = "  "
	appURLGutter = "  "
)

type summary struct {
	result        *streamv1.RunResultEvent
	tier          environmentv1.Tier
	promotion     string
	changeStarted bool
	present       Presentation
}

func (s summary) lines() []string {
	result := s.result
	took := formatDuration(time.Duration(result.GetDurationMs()) * time.Millisecond)
	var out []string
	if result.GetSuccess() {
		out = s.succeeded(took)
	} else {
		out = s.failed(took)
	}
	if path := result.GetLogPath(); path != "" {
		out = append(out, colorFor(s.present, color.Faint).Sprint(blockIndent+"Log: "+relLog(path)))
	}
	return out
}

func (s summary) succeeded(took string) []string {
	result := s.result
	head := colorFor(s.present, color.FgGreen, color.Bold).Sprintf("%s %s in %s", okMark, headlineOr(result, "Done"), took)
	out := []string{head}
	if place := servedPlace(s.tier); place != "" && s.promotion != "" {
		out = append(out, blockIndent+place+" now serves promotion "+s.promotion)
	}
	out = append(out, s.appURLs()...)
	for _, note := range result.GetUrlNotes() {
		out = append(out, blockIndent+note)
	}
	if note := FlipNote(result.GetFlipBound()); note != "" {
		out = append(out, colorFor(s.present, color.Faint).Sprint(blockIndent+note))
	}
	return out
}

func (s summary) failed(took string) []string {
	result := s.result
	if missing := result.GetMissing(); missing != nil {
		out := append(envgate.Lines(missing, missingPaint(s.present)), "", envgate.RemedyLine(missing.GetRemedy()))
		return append(out, detailLines(result.GetDetail())...)
	}
	detail := strings.Split(strings.TrimRight(result.GetDetail(), "\n"), "\n")
	head := fmt.Sprintf("%s %s in %s", failMark, headlineOr(result, "Failed"), took)
	if detail[0] != "" {
		head += " — " + detail[0]
	}
	out := []string{colorFor(s.present, color.FgRed, color.Bold).Sprint(head)}
	for _, line := range detail[1:] {
		out = append(out, blockIndent+line)
	}
	if unpromoted := s.unpromoted(); len(unpromoted) > 0 {
		return append(out, unpromoted...)
	}
	if place := servedPlace(s.tier); place != "" && !s.changeStarted {
		out = append(out, blockIndent+"nothing was changed; "+place+" still serves what it served before this run")
	}
	return out
}

func (s summary) unpromoted() []string {
	place := servedPlace(s.tier)
	var deployed, failed, notRun []string
	for _, app := range s.result.GetApps() {
		switch app.GetOutcome() {
		case progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED:
			deployed = append(deployed, app.GetApp())
		case progressv1.AppOutcome_APP_OUTCOME_FAILED:
			failed = append(failed, app.GetApp())
		case progressv1.AppOutcome_APP_OUTCOME_NOT_RUN:
			notRun = append(notRun, app.GetApp())
		}
	}
	if place == "" || len(failed)+len(notRun) == 0 {
		return nil
	}
	var why []string
	if len(failed) > 0 {
		why = append(why, joinNames(failed)+" failed")
	}
	if len(notRun) > 0 {
		why = append(why, joinNames(notRun)+" did not run")
	}
	reason := strings.Join(why, " and ")
	if len(s.result.GetApps()) > 1 {
		reason = "promotion needs every app, and " + reason
	}
	what := "nothing was promoted"
	switch {
	case len(deployed) == 1:
		what = deployed[0] + " deployed but was not promoted"
	case len(deployed) > 1:
		what = joinNames(deployed) + " deployed but were not promoted"
	}
	return []string{
		blockIndent + what + ": " + reason,
		blockIndent + place + " still serves what it served before this run",
	}
}

func missingPaint(present Presentation) envgate.Paint {
	return envgate.Paint{
		Fail:  func(text string) string { return colorFor(present, color.FgRed).Sprint(text) },
		Faint: func(text string) string { return faint(present, text) },
	}
}

func joinNames(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func servedPlace(tier environmentv1.Tier) string {
	switch tier {
	case environmentv1.Tier_TIER_PRODUCTION:
		return "production"
	case environmentv1.Tier_TIER_PREVIEW:
		return "the preview"
	}
	return ""
}

func (s summary) appURLs() []string {
	apps := s.result.GetApps()
	var width int
	for _, app := range apps {
		width = max(width, utf8.RuneCountInString(app.GetApp()))
	}
	url := colorFor(s.present, color.FgCyan)
	var out []string
	for _, app := range apps {
		label := blockIndent + app.GetApp() + strings.Repeat(" ", width-utf8.RuneCountInString(app.GetApp())) + appURLGutter
		gutter := blockIndent + strings.Repeat(" ", width) + appURLGutter
		for at, u := range app.GetUrls() {
			lead := label
			if at > 0 {
				lead = gutter
			}
			out = append(out, lead+url.Sprint(u))
		}
	}
	return out
}

func (s *GroupedSink) conclude(ev *streamv1.RunEvent) {
	s.unfinished()
	s.gap()
	for _, text := range (summary{result: ev.GetResult(), tier: s.tier, promotion: s.promotion, changeStarted: s.changeStarted, present: s.present}).lines() {
		s.print(blockLine{text: text})
	}
}

func headlineOr(ev *streamv1.RunResultEvent, fallback string) string {
	if h := ev.GetHeadline(); h != "" {
		return h
	}
	return fallback
}

func detailLines(detail string) []string {
	if detail == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimRight(detail, "\n"), "\n") {
		out = append(out, blockIndent+line)
	}
	return out
}

func relLog(logPath string) string {
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, logPath); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return logPath
}
