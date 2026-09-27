package runui

import (
	"io"
	"os"

	"github.com/fatih/color"
)

type Format string

const (
	FormatHuman Format = "human"
	FormatJSON  Format = "json"
)

type Origin struct {
	LogFormat     Format
	Verbose       bool
	NoColor       bool
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
		Color:         o.TTY && !o.NoColor,
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
		NoColor:       color.NoColor || os.Getenv("NO_COLOR") != "",
		TTY:           IsTerminal(w),
		Width:         termWidth(w),
		WidthMeasured: measured,
		Dumb:          os.Getenv("TERM") == "dumb",
		GitHubActions: os.Getenv("GITHUB_ACTIONS") == "true",
	})
}
