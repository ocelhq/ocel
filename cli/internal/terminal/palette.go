package terminal

import (
	"hash/fnv"
	"io"
	"strings"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"

	"charm.land/lipgloss/v2"
	"github.com/fatih/color"
)

const (
	passGlyph    = "✓"
	failGlyph    = "✗"
	warnGlyph    = "⚠"
	neutralGlyph = "–"
)

var (
	pillBackground = lipgloss.ANSIColor(6)
	pillForeground = lipgloss.ANSIColor(0)
	failureBorder  = lipgloss.Red
)

type Palette struct {
	colored bool
}

func PaletteFor(w io.Writer) Palette {
	return Palette{colored: Detect(FormatHuman, false, w).Color}
}

func (p Presentation) Palette() Palette {
	return Palette{colored: p.Color}
}

func (p Palette) Bold(text string) string { return p.paint(text, color.Bold) }

func (p Palette) Faint(text string) string { return p.paint(text, color.Faint) }

func (p Palette) Muted(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = p.paint(line, color.FgHiBlack)
		}
	}
	return strings.Join(lines, "\n")
}

func (p Palette) Success(text string) string { return p.paint(text, color.FgGreen) }

func (p Palette) SuccessBold(text string) string { return p.paint(text, color.FgGreen, color.Bold) }

func (p Palette) Warning(text string) string { return p.paint(text, color.FgYellow) }

func (p Palette) WarningBold(text string) string { return p.paint(text, color.FgYellow, color.Bold) }

func (p Palette) Failure(text string) string { return p.paint(text, color.FgRed) }

func (p Palette) FailureBold(text string) string { return p.paint(text, color.FgRed, color.Bold) }

func (p Palette) Accent(text string) string { return p.paint(text, color.FgCyan) }

func (p Palette) Link(text string) string { return p.paint(text, color.FgCyan, color.Underline) }

func (p Palette) PassMark() string { return p.Success(passGlyph) }

func (p Palette) FailMark() string { return p.FailureBold(failGlyph) }

func (p Palette) WarnMark() string { return p.Warning(warnGlyph) }

func (p Palette) NeutralMark() string { return p.Faint(neutralGlyph) }

var hues = [...]color.Attribute{color.FgGreen, color.FgBlue, color.FgMagenta, color.FgCyan, color.FgHiBlue, color.FgHiMagenta}

func (p Palette) Hue(key, text string) string {
	hash := fnv.New32a()
	hash.Write([]byte(key))
	return p.paint(text, hues[hash.Sum32()%uint32(len(hues))])
}

func (p Palette) FailureBox() lipgloss.Style {
	style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	if p.colored {
		style = style.BorderForeground(failureBorder)
	}
	return style
}

func (p Palette) Commands(text string) string {
	parts := strings.Split(text, "`")
	if len(parts)%2 == 0 {
		return text
	}
	for i := 1; i < len(parts); i += 2 {
		parts[i] = p.Accent("`" + parts[i] + "`")
	}
	return strings.Join(parts, "")
}

func (p Palette) pill() lipgloss.Style {
	if !p.colored {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Background(pillBackground).Foreground(pillForeground).Bold(true).Padding(0, 1)
}

func (p Palette) paint(text string, attrs ...color.Attribute) string {
	c := color.New(attrs...)
	if p.colored {
		c.EnableColor()
	} else {
		c.DisableColor()
	}
	return c.Sprint(text)
}

type label struct {
	text  string
	attrs []color.Attribute
}

var levelLabels = map[progressv1.Level]label{
	progressv1.Level_LEVEL_DEBUG: {"DEBUG", []color.Attribute{color.Faint, color.FgHiBlack}},
	progressv1.Level_LEVEL_INFO:  {"INFO ", []color.Attribute{color.FgHiBlack}},
	progressv1.Level_LEVEL_WARN:  {"WARN ", []color.Attribute{color.FgYellow, color.Bold}},
	progressv1.Level_LEVEL_ERROR: {"ERROR", []color.Attribute{color.FgRed, color.Bold}},
}

var spanMarks = map[progressv1.SpanStatus]label{
	progressv1.SpanStatus_SPAN_STATUS_OK:    {passGlyph, []color.Attribute{color.FgGreen}},
	progressv1.SpanStatus_SPAN_STATUS_ERROR: {failGlyph, []color.Attribute{color.FgRed, color.Bold}},
}

func (l label) render(present Presentation) string {
	if len(l.attrs) == 0 {
		return l.text
	}
	return present.Palette().paint(l.text, l.attrs...)
}

var sigilAttrs = map[string][]color.Attribute{
	"+": {color.FgGreen},
	"~": {color.FgYellow},
	"±": {color.FgYellow},
	"–": {color.FgRed},
}
