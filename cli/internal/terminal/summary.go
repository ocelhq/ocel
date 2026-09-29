package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ocelhq/ocel/cli/internal/english"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const (
	blockIndent  = "  "
	appURLGutter = "  "
)

type summary struct {
	result  *streamv1.RunSummary
	present Presentation
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
		out = append(out, s.present.palette().Muted(blockIndent+"Log: "+relLog(path)))
	}
	return out
}

func (s summary) succeeded(took string) []string {
	result := s.result
	head := s.present.palette().SuccessBold(fmt.Sprintf("%s %s in %s", passGlyph, headlineOr(result, "Done"), took))
	out := []string{head}
	if target := targetLine(s.present, result.GetOrigin()); target != "" {
		out = append(out, target)
	}
	if place := servedPlace(result.GetTier()); place != "" && result.GetPromotionId() != "" {
		out = append(out, blockIndent+place+" now serves promotion "+result.GetPromotionId())
	}
	out = append(out, s.appURLs()...)
	for _, note := range result.GetUrlNotes() {
		out = append(out, blockIndent+note)
	}
	if note := PropagationNote(result.GetPropagation()); note != "" {
		out = append(out, s.present.palette().Muted(blockIndent+note))
	}
	return out
}

func (s summary) failed(took string) []string {
	result := s.result
	if missing := result.GetMissing(); missing != nil {
		out := append(MissingVariablesLines(missing, s.present), "", MissingVariablesRemedy(missing.GetRemedy()))
		return append(out, detailLines(result.GetDetail())...)
	}
	out := failureLines(s.present.palette(), headlineOr(result, "Failed")+" in "+took, result.GetDetail())
	if target := targetLine(s.present, result.GetOrigin()); target != "" {
		out = append(out, target)
	}
	if unpromoted := s.unpromoted(); len(unpromoted) > 0 {
		return append(out, unpromoted...)
	}
	if place := servedPlace(result.GetTier()); place != "" && !result.GetChangeStarted() {
		out = append(out, blockIndent+"nothing was changed; "+place+" still serves what it served before this run")
	}
	return out
}

func (s summary) unpromoted() []string {
	place := servedPlace(s.result.GetTier())
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
		why = append(why, english.And(failed)+" failed")
	}
	if len(notRun) > 0 {
		why = append(why, english.And(notRun)+" did not run")
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
		what = english.And(deployed) + " deployed but were not promoted"
	}
	return []string{
		blockIndent + what + ": " + reason,
		blockIndent + place + " still serves what it served before this run",
	}
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
	p := s.present.palette()
	var out []string
	for _, app := range apps {
		label := blockIndent + app.GetApp() + strings.Repeat(" ", width-utf8.RuneCountInString(app.GetApp())) + appURLGutter
		gutter := blockIndent + strings.Repeat(" ", width) + appURLGutter
		for at, u := range app.GetUrls() {
			lead := label
			if at > 0 {
				lead = gutter
			}
			out = append(out, lead+p.Accent(u))
		}
	}
	return out
}

func (s *Transcript) printSummary(ev *streamv1.RunEvent) {
	s.printUnfinished()
	s.gap()
	result := summary{result: ev.GetSummary(), present: s.present}
	for _, text := range result.lines() {
		s.print(blockLine{text: text})
	}
	if !result.result.GetSuccess() {
		s.annotate(ev.GetLevel(), result.annotation())
	}
}

func (s summary) annotation() string {
	result := s.result
	if missing := result.GetMissing(); missing != nil {
		text := headlineOr(result, "Failed") + ": " + MissingVariablesHeadline(len(missing.GetCells())) +
			"\n" + strings.TrimLeft(MissingVariablesRemedy(missing.GetRemedy()), " ")
		if detail := strings.TrimRight(result.GetDetail(), "\n"); detail != "" {
			text += "\n" + detail
		}
		return text
	}
	text := headlineOr(result, "Failed")
	if detail := strings.TrimRight(result.GetDetail(), "\n"); detail != "" {
		text += " — " + detail
	}
	return text
}

func headlineOr(ev *streamv1.RunSummary, fallback string) string {
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
