package dev

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

func reportUnreadableLines(stdout io.Writer, values valueLayers) {
	for _, layer := range values {
		if len(layer.unreadable) == 0 {
			continue
		}
		numbers := make([]string, 0, len(layer.unreadable))
		for _, line := range layer.unreadable {
			numbers = append(numbers, strconv.Itoa(line))
		}
		if len(layer.unreadable) == 1 {
			fmt.Fprintf(stdout, "%s line %s is not KEY=VALUE and was ignored.\n", layer.from, numbers[0])
			continue
		}
		fmt.Fprintf(stdout, "%s lines %s are not KEY=VALUE and were ignored.\n", layer.from, strings.Join(numbers, ", "))
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
		return fmt.Sprintf("editing %s re-resolves this run; saving it is enough.", strings.Join(files, " or "))
	}
	return fmt.Sprintf("%s is read once, at startup; editing it takes effect on the next `ocel run`.", strings.Join(files, " or "))
}

func reportValues(stdout io.Writer, dir string, values valueLayers, watched bool) {
	reported := false
	for _, layer := range values {
		if len(layer.values) == 0 {
			continue
		}
		reported = true
		keys := strings.Join(slices.Sorted(maps.Keys(layer.values)), ", ")
		if !layer.file {
			fmt.Fprintf(stdout, "resolved %s from %s, the dev env source, read once as this run started.\n", keys, layer.from)
			continue
		}
		fmt.Fprintf(stdout, "resolved %s from %s. That file is yours alone — a teammate's checkout has its own, so nothing set here reaches anyone else and a deploy resolves none of it.\n",
			keys, layer.from)
	}
	if !reported {
		return
	}
	fmt.Fprintf(stdout, "dev delivers every value to the app in plaintext under its own name; a deploy keeps a sensitive value out of the function environment and a live one out of the artifact.\n")
	fmt.Fprintln(stdout, values.advice(watched))
	for _, layer := range values {
		if !layer.file || len(layer.values) == 0 {
			continue
		}
		switch ignored, err := readGitIgnored(dir, layer.from); {
		case err != nil:
			fmt.Fprintf(stdout, "ocel cannot tell whether git ignores %s (%v). Keep it out of version control — it contains values nothing else may see.\n", layer.from, err)
		case !ignored:
			fmt.Fprintf(stdout, "%s is not ignored by git. Add it to .gitignore before committing — it contains values nothing else may see.\n", layer.from)
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

func reportSecretValues(stdout io.Writer, secretKeys []string) {
	if len(secretKeys) == 0 {
		return
	}
	keys := slices.Clone(secretKeys)
	slices.Sort(keys)
	fmt.Fprintf(stdout, "resolved %s the way dev resolves every other value. Deployed, a rotated value is picked up within a bounded window.\n", strings.Join(keys, ", "))
}
