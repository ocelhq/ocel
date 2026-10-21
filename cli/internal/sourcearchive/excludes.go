package sourcearchive

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

const gitConfigUnset = 1

func readExcludes(root string) ([]gitignore.Pattern, error) {
	globalFile, err := globalExcludesFile(root)
	if err != nil {
		return nil, err
	}
	var global []gitignore.Pattern
	if globalFile != "" {
		if global, err = readGitignore(globalFile, nil); err != nil {
			return nil, err
		}
	}
	local, err := readGitignore(filepath.Join(root, gitEntry, "info", "exclude"), nil)
	if err != nil {
		return nil, err
	}
	return slices.Concat(credentialPatterns, global, local), nil
}

func globalExcludesFile(root string) (string, error) {
	read, err := exec.Command("git", "-C", root, "config", "--path", "--get", "core.excludesFile").Output()
	var exit *exec.ExitError
	switch {
	case err == nil:
		file := string(bytes.TrimSpace(read))
		if !filepath.IsAbs(file) {
			file = filepath.Join(root, file)
		}
		return file, nil
	case errors.Is(err, exec.ErrNotFound), errors.As(err, &exit) && exit.ExitCode() == gitConfigUnset:
		return defaultGlobalExcludesFile(), nil
	case exit != nil:
		return "", fmt.Errorf("read git's core.excludesFile for %s: %s", root, bytes.TrimSpace(exit.Stderr))
	}
	return "", fmt.Errorf("read git's core.excludesFile for %s: %w", root, err)
}

func defaultGlobalExcludesFile() string {
	if config := os.Getenv("XDG_CONFIG_HOME"); config != "" {
		return filepath.Join(config, "git", "ignore")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "git", "ignore")
	}
	return ""
}
