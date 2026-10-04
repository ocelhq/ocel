package terminal

import (
	"io"
	"os"
)

type Format int

const (
	FormatHuman Format = iota
	FormatJSON
)

type ColorChoice int

const (
	ColorAuto ColorChoice = iota
	ColorNever
	ColorAlways
)

type Conditions struct {
	Format        Format
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

const minLiveColumns = 40

func Resolve(o Conditions) Presentation {
	p := Presentation{
		Format:        o.Format,
		Verbose:       o.Verbose,
		Color:         o.ColorAsked == ColorAlways || o.ColorAsked == ColorAuto && (o.TTY && !o.Dumb || o.GitHubActions),
		TTY:           o.TTY,
		Dumb:          o.Dumb,
		Width:         o.Width,
		WidthMeasured: o.WidthMeasured,
		GitHubActions: o.GitHubActions,
	}
	if p.Width <= 0 {
		p.Width = defaultColumns
	}
	return p
}

func (p Presentation) Live() bool {
	return p.Format == FormatHuman && !p.Verbose && p.TTY && !p.Dumb && !p.SharedTerminal && p.WidthMeasured && p.Width >= minLiveColumns
}

func Detect(format Format, verbose bool, w io.Writer) Presentation {
	_, measured := liveWidth(w)
	return Resolve(Conditions{
		Format:        format,
		Verbose:       verbose,
		ColorAsked:    colorAsked(),
		TTY:           IsTerminal(w),
		Width:         termWidth(w),
		WidthMeasured: measured,
		Dumb:          os.Getenv("TERM") == "dumb",
		GitHubActions: os.Getenv("GITHUB_ACTIONS") == "true",
	})
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
