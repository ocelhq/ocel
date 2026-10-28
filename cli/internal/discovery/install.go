package discovery

import (
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/workdir"
	"github.com/ocelhq/ocel/cli/internal/workspace"
	"github.com/ocelhq/ocel/pkg/progress"
)

const installTailBytes = 4 << 10

func installMissingDependencies(ctx context.Context, configDir string, roots []Root, output io.Writer) error {
	missing, err := findMissingInstalls(roots)
	if err != nil || len(missing) == 0 {
		return err
	}
	release, err := workdir.LockDependencyInstall(ctx, configDir, func() {
		_, _ = fmt.Fprintln(output, "Waiting for another ocel run in "+configDir+" to finish installing its dependencies")
	})
	if err != nil {
		return err
	}
	defer release()
	if missing, err = findMissingInstalls(roots); err != nil {
		return err
	}
	for _, install := range missing {
		if err := runInstall(ctx, install, output); err != nil {
			return err
		}
	}
	return nil
}

func findMissingInstalls(roots []Root) ([]workspace.Install, error) {
	var missing []workspace.Install
	seen := map[string]bool{}
	for _, root := range roots {
		if root.Language != language.JS {
			continue
		}
		install, ok, err := workspace.FindMissingInstall(root.Dir)
		if err != nil {
			return nil, err
		}
		if ok && !seen[install.Dir] {
			seen[install.Dir] = true
			missing = append(missing, install)
		}
	}
	return missing, nil
}

func runInstall(ctx context.Context, install workspace.Install, output io.Writer) error {
	command := install.Command()
	_, _ = fmt.Fprintln(output, progress.Installing.Title("the dependencies in "+install.Dir+" with `"+command+"`").Started)
	tail := &childprocess.Tail{Limit: installTailBytes}
	cmd := exec.Command(install.Argv[0], install.Argv[1:]...)
	cmd.Dir = install.Dir
	cmd.Stdout, cmd.Stderr = tail, tail
	if err := childprocess.Run(ctx, cmd); err != nil {
		return fmt.Errorf("the dependencies in %s are not installed, and `%s` failed (%w):\n%s\nrun `%s` there once what it printed is fixed", install.Dir, command, err, tail.String(), command)
	}
	return nil
}
