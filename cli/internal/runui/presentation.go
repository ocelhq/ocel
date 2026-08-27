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
	LogFormat Format
	Verbose   bool
	NoColor   bool
	TTY       bool
	Width     int
	Height    int
}

type Presentation struct {
	Format  Format
	Verbose bool
	Color   bool
	TTY     bool
	Width   int
	Height  int
}

func Resolve(o Origin) Presentation {
	p := Presentation{
		Format:  FormatHuman,
		Verbose: o.Verbose,
		Color:   o.TTY && !o.NoColor,
		TTY:     o.TTY,
		Width:   o.Width,
		Height:  o.Height,
	}
	if o.LogFormat == FormatJSON {
		p.Format = FormatJSON
	}
	if p.Width <= 0 {
		p.Width = defaultWidth
	}
	if p.Height <= 0 {
		p.Height = defaultHeight
	}
	return p
}

func (p Presentation) Live() bool {
	return p.Format == FormatHuman && !p.Verbose && p.TTY
}

func Detect(logFormat Format, verbose bool, w io.Writer) Presentation {
	return Resolve(Origin{
		LogFormat: logFormat,
		Verbose:   verbose,
		NoColor:   color.NoColor || os.Getenv("NO_COLOR") != "",
		TTY:       IsTerminal(w),
		Width:     termWidth(w),
		Height:    termHeight(w),
	})
}
