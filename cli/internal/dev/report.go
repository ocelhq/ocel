package dev

import (
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/run"
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

func (v valueLayers) advice(watched bool) string {
	var files []string
	for _, layer := range v {
		if layer.file {
			files = append(files, layer.from)
		}
	}
	if watched {
		return fmt.Sprintf("Editing %s re-resolves this run.", strings.Join(files, " or "))
	}
	return fmt.Sprintf("%s is read once; edits apply on the next `ocel run`.", strings.Join(files, " or "))
}

func reportValues(span *run.Span, dir string, values valueLayers, watched bool) {
	reported := false
	for _, layer := range values {
		if len(layer.values) == 0 {
			continue
		}
		reported = true
		keys := strings.Join(slices.Sorted(maps.Keys(layer.values)), ", ")
		if !layer.file {
			span.Say(fmt.Sprintf("Resolved %s from %s.", keys, layer.from))
			continue
		}
		span.Say(fmt.Sprintf("Resolved %s from %s, local to this checkout.", keys, layer.from))
	}
	if !reported {
		return
	}
	span.Say(values.advice(watched))
	for _, layer := range values {
		if !layer.file || len(layer.values) == 0 {
			continue
		}
		switch ignored, err := readGitIgnored(dir, layer.from); {
		case err != nil:
			span.Warn(fmt.Sprintf("Could not tell whether git ignores %s (%v): keep it out of version control.", layer.from, err))
		case !ignored:
			span.Warn(fmt.Sprintf("%s is not ignored by git: add it to .gitignore before committing.", layer.from))
		}
	}
}

func readGitIgnored(dir, name string) (bool, error) {
	err := exec.Command("git", "-C", dir, "check-ignore", "--quiet", "--", name).Run()
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

func reportSecretValues(span *run.Span, secretKeys []string) {
	if len(secretKeys) == 0 {
		return
	}
	keys := slices.Clone(secretKeys)
	slices.Sort(keys)
	span.Say(fmt.Sprintf("Resolved secrets %s.", strings.Join(keys, ", ")))
}
