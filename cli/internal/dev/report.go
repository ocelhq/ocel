package dev

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/tracing"
)

func reportUnreadableLines(span *run.Span, values valueLayers) {
	for _, layer := range values {
		if len(layer.unreadable) == 0 {
			continue
		}
		numbers := make([]string, 0, len(layer.unreadable))
		for _, line := range layer.unreadable {
			numbers = append(numbers, strconv.Itoa(line))
		}
		if len(layer.unreadable) == 1 {
			span.Warn(fmt.Sprintf("%s line %s is not KEY=VALUE and was ignored.", layer.from, numbers[0]))
			continue
		}
		span.Warn(fmt.Sprintf("%s lines %s are not KEY=VALUE and were ignored.", layer.from, strings.Join(numbers, ", ")))
	}
}

func (v valueLayers) summary() string {
	seen := map[string]struct{}{}
	contributes := make([]bool, len(v))
	for i := len(v) - 1; i >= 0; i-- {
		for key := range v[i].values {
			if _, overridden := seen[key]; overridden {
				continue
			}
			seen[key] = struct{}{}
			contributes[i] = true
		}
	}
	var sources []string
	for i, layer := range v {
		if contributes[i] {
			sources = append(sources, layer.from)
		}
	}
	unit := "variables"
	if len(seen) == 1 {
		unit = "variable"
	}
	summary := fmt.Sprintf("%d %s", len(seen), unit)
	if len(sources) > 0 {
		summary += " from " + strings.Join(sources, ", ")
	}
	return summary
}

func warnUnignoredFiles(ctx context.Context, span *run.Span, dir string, values valueLayers) {
	for _, layer := range values {
		if !layer.file || len(layer.values) == 0 {
			continue
		}
		switch ignored, err := readGitIgnored(run.ContextWithSpan(ctx, span), dir, layer.from); {
		case err != nil:
			span.Warn(fmt.Sprintf("Could not check whether git ignores %s: %v", layer.from, err))
		case !ignored:
			span.Warn(fmt.Sprintf("%s is not ignored by git; add it to .gitignore.", layer.from))
		}
	}
}

func readGitIgnored(ctx context.Context, dir, name string) (bool, error) {
	err := tracing.Command(ctx, "git", "-C", dir, "check-ignore", "--quiet", "--", name).Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	case errors.As(err, &exit):
		return false, fmt.Errorf("%s is not in a git repository", dir)
	default:
		return false, fmt.Errorf("git did not run: %w", err)
	}
}
