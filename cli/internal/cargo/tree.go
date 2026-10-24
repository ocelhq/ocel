package cargo

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
)

const buildDependenciesHeading = "[build-dependencies]"

func ListPackagesBuildingC(ctx context.Context, dir, manifestPath, target string, environ []string) ([]string, error) {
	forTarget, err := readTree(ctx, dir, manifestPath, target, environ, "normal,no-proc-macro", "--prefix", "none")
	if err != nil {
		return nil, err
	}
	builtForTarget := map[string]bool{}
	for _, line := range forTarget {
		builtForTarget[strings.TrimSuffix(line, " (*)")] = true
	}
	graph, err := readTree(ctx, dir, manifestPath, target, environ, "normal,build,no-proc-macro")
	if err != nil {
		return nil, err
	}
	var names, ancestors []string
	headings := map[int]string{}
	for _, line := range graph {
		indent := len(line) - len(strings.TrimLeft(line, " |`-"))
		depth := indent / 4
		entry := line[indent:]
		if strings.HasPrefix(entry, "[") {
			headings[depth] = entry
			continue
		}
		name := strings.TrimSuffix(entry, " (*)")
		ancestors = append(ancestors[:min(depth, len(ancestors))], name)
		delete(headings, depth)
		if depth == 0 {
			continue
		}
		parent := ancestors[depth-1]
		if strings.HasPrefix(name, "cc v") && headings[depth-1] == buildDependenciesHeading && builtForTarget[parent] {
			names = append(names, strings.Fields(parent)[0])
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

func readTree(ctx context.Context, dir, manifestPath, target string, environ []string, edges string, opts ...string) ([]string, error) {
	args := append([]string{"tree", "--quiet", "--manifest-path", manifestPath, "--target", target, "--edges", edges, "--charset", "ascii", "--format", "{p}"}, opts...)
	cmd := exec.Command("cargo", args...)
	cmd.Dir = dir
	cmd.Env = environ
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := childprocess.Run(ctx, cmd); err != nil {
		if said := strings.TrimSpace(stderr.String()); said != "" {
			return nil, fmt.Errorf("cargo tree of %s for %s: %s", manifestPath, target, said)
		}
		return nil, fmt.Errorf("cargo tree of %s for %s: %w", manifestPath, target, err)
	}
	return strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n"), nil
}
