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
	lines, err := readTree(ctx, dir, manifestPath, target, environ)
	if err != nil {
		return nil, err
	}
	var names, ancestors []string
	var builtForTarget []bool
	headings := map[int]string{}
	for _, line := range lines {
		indent := len(line) - len(strings.TrimLeft(line, " |`-"))
		depth := indent / 4
		entry := line[indent:]
		if strings.HasPrefix(entry, "[") {
			headings[depth] = entry
			continue
		}
		name := strings.TrimSuffix(entry, " (*)")
		delete(headings, depth)
		forTarget := true
		if depth > 0 {
			buildEdge := headings[depth-1] == buildDependenciesHeading
			if buildEdge && builtForTarget[depth-1] && strings.HasPrefix(name, "cc v") {
				names = append(names, strings.Fields(ancestors[depth-1])[0])
			}
			forTarget = builtForTarget[depth-1] && !buildEdge
		}
		ancestors = append(ancestors[:depth], name)
		builtForTarget = append(builtForTarget[:depth], forTarget)
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

func readTree(ctx context.Context, dir, manifestPath, target string, environ []string) ([]string, error) {
	cmd := exec.Command("cargo", "tree", "--quiet", "--manifest-path", manifestPath, "--target", target,
		"--edges", "normal,build,no-proc-macro", "--charset", "ascii", "--format", "{p}")
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
