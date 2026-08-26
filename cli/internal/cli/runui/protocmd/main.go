package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mattn/go-isatty"
	"golang.org/x/term"

	"github.com/ocelhq/ocel/cli/internal/cli/runui"
)

func main() {
	variant := flag.String("variant", "vps", "vps | aws-container | aws-serverless")
	mode := flag.String("mode", "auto", "auto | live | plain | json")
	dry := flag.Bool("dry", false, "render the plan and stop, as `deploy --dry` does")
	fail := flag.Bool("fail", false, "one app fails: siblings finish, promotion is withheld")
	speed := flag.Float64("speed", 1, "playback multiplier; 0 renders the final frame with no waiting")
	maxRows := flag.Int("max-rows", 20, "row budget for the live window")
	flag.Parse()

	plan, apply, ok := pick(*variant, *fail)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown variant %q\n", *variant)
		os.Exit(2)
	}

	out := os.Stdout
	tty := isatty.IsTerminal(out.Fd())
	format := resolve(*mode, tty)

	width, height := 100, 40
	if w, h, err := term.GetSize(int(out.Fd())); err == nil && w > 0 {
		width, height = w, h
	}

	r := runui.New(out, runui.Config{
		Format:  format,
		Color:   tty && os.Getenv("NO_COLOR") == "",
		Width:   width,
		Height:  height,
		MaxRows: *maxRows,
	})

	r.Emit(runui.Envelope{Plan: plan})
	if *dry {
		if format != runui.NDJSON {
			fmt.Fprintln(out)
			fmt.Fprintln(out, "  Nothing was applied. Drop --dry to deploy.")
		}
		return
	}

	if format != runui.NDJSON && runui.Moving(plan) {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "  Deploying — this plan is convergent, so it applies without asking.")
		fmt.Fprintln(out)
	}

	r.Start()
	play(r, apply, *speed)
}

func pick(variant string, fail bool) (*runui.Plan, *script, bool) {
	switch variant {
	case "vps":
		return vpsPlan(), vpsApply(fail), true
	case "aws-container":
		return awsContainerPlan(), awsContainerApply(fail), true
	case "aws-serverless":
		return awsServerlessPlan(), awsServerlessApply(), true
	default:
		return nil, nil, false
	}
}

func resolve(mode string, tty bool) runui.Format {
	switch mode {
	case "live":
		return runui.Live
	case "plain":
		return runui.Plain
	case "json":
		return runui.NDJSON
	default:
		if tty {
			return runui.Live
		}
		return runui.Plain
	}
}

func play(r *runui.Renderer, s *script, speed float64) {
	var elapsed time.Duration
	for _, env := range s.ev {
		if speed > 0 {
			if gap := env.At - elapsed; gap > 0 {
				time.Sleep(time.Duration(float64(gap) / speed))
			}
		}
		elapsed = env.At
		r.Emit(env)
	}
}
