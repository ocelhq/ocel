package projectinit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/statedir"
)

const gitignoreName = ".gitignore"

const stateDirPattern = statedir.Name + "/"

var stateDirPatterns = []string{
	statedir.Name, statedir.Name + "/", statedir.Name + "/*", statedir.Name + "/**",
	"/" + statedir.Name, "/" + statedir.Name + "/", "/" + statedir.Name + "/*", "/" + statedir.Name + "/**",
}

type ignoreStatus int

const (
	ignored ignoreStatus = iota
	notIgnored
	outsideRepository
)

func ensureStateDirIgnored(projectDir string) (bool, error) {
	status := readStateDirIgnoreStatus(projectDir)
	if status == ignored {
		return false, nil
	}
	path := filepath.Join(projectDir, gitignoreName)
	content, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if status == outsideRepository {
			return false, nil
		}
	case err != nil:
		return false, fmt.Errorf("read %s: %w", gitignoreName, err)
	case listsStateDir(content):
		return false, nil
	}

	addition := stateDirPattern + "\n"
	if len(content) > 0 && content[len(content)-1] != '\n' {
		addition = "\n" + addition
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", gitignoreName, err)
	}
	_, err = file.WriteString(addition)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false, fmt.Errorf("write %s: %w", gitignoreName, err)
	}
	return true, nil
}

func readStateDirIgnoreStatus(projectDir string) ignoreStatus {
	err := exec.Command("git", "-C", projectDir, "check-ignore", "--quiet", "--", stateDirPattern).Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return ignored
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return notIgnored
	}
	return outsideRepository
}

func listsStateDir(content []byte) bool {
	for line := range strings.Lines(string(content)) {
		if slices.Contains(stateDirPatterns, strings.TrimSpace(line)) {
			return true
		}
	}
	return false
}
