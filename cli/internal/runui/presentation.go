package runui

import (
	"io"
	"os"
)

type Format string

const (
	FormatHuman Format = "human"
	FormatJSON  Format = "json"
)

type ColorChoice int

const (
	ColorAuto ColorChoice = iota
	ColorNever
	ColorAlways
)

type Origin struct {
	LogFormat     Format
	Verbose       bool
	ColorAsked    ColorChoice
	TTY           bool
	Width         int
	WidthMeasured bool
	Dumb          bool
	GitHubActions bool
}

type Presentation struct {
	Format         Format
	Verbose        bool
	Color          bool
	TTY            bool
	Dumb           bool
	Width          int
	WidthMeasured  bool
	GitHubActions  bool
	SharedTerminal bool
}

const minLiveWidth = 40

func Resolve(o Origin) Presentation {
	p := Presentation{
		Format:        FormatHuman,
		Verbose:       o.Verbose,
		Color:         o.ColorAsked == ColorAlways || o.ColorAsked == ColorAuto && (o.TTY && !o.Dumb || o.GitHubActions),
		TTY:           o.TTY,
		Dumb:          o.Dumb,
		Width:         o.Width,
		WidthMeasured: o.WidthMeasured,
		GitHubActions: o.GitHubActions,
	}
	if o.LogFormat == FormatJSON {
		p.Format = FormatJSON
	}
	if p.Width <= 0 {
		p.Width = defaultWidth
	}
	return p
}

func (p Presentation) Live() bool {
	return p.Format == FormatHuman && !p.Verbose && p.TTY && !p.Dumb && !p.SharedTerminal && p.WidthMeasured && p.Width >= minLiveWidth
}

func Detect(logFormat Format, verbose bool, w io.Writer) Presentation {
	_, measured := liveWidth(w)
	return Resolve(Origin{
		LogFormat:     logFormat,
		Verbose:       verbose,
		ColorAsked:    colorAsked(),
		TTY:           IsTerminal(w),
		Width:         termWidth(w),
		WidthMeasured: measured,
		Dumb:          os.Getenv("TERM") == "dumb",
		GitHubActions: os.Getenv("GITHUB_ACTIONS") == "true",
	})
}

func IsColored(w io.Writer) bool {
	return Detect(FormatHuman, false, w).Color
}

func colorAsked() ColorChoice {
	if os.Getenv("NO_COLOR") != "" {
		return ColorNever
	}
	switch os.Getenv("FORCE_COLOR") {
	case "":
	case "0", "false":
		return ColorNever
	default:
		return ColorAlways
	}
	if force := os.Getenv("CLICOLOR_FORCE"); force != "" && force != "0" {
		return ColorAlways
	}
	return ColorAuto
}
