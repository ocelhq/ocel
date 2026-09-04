package imagebuild

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/moby/patternmatcher/ignorefile"
	"github.com/tonistiigi/fsutil"
)

const DockerignoreName = ".dockerignore"

var neverInTheContext = []string{"node_modules", ".git", ".ocel"}

func contextFS(root string) (fsutil.FS, error) {
	source, err := fsutil.NewFS(root)
	if err != nil {
		return nil, fmt.Errorf("read %s as a build context: %w", root, err)
	}
	excludes, err := contextExcludes(root)
	if err != nil {
		return nil, err
	}
	return fsutil.NewFilterFS(source, &fsutil.FilterOpt{ExcludePatterns: excludes})
}

func contextExcludes(root string) ([]string, error) {
	excludes := make([]string, 0, len(neverInTheContext))
	for _, name := range neverInTheContext {
		excludes = append(excludes, "**/"+name)
	}
	file, err := os.Open(filepath.Join(root, DockerignoreName))
	if err != nil {
		if os.IsNotExist(err) {
			return excludes, nil
		}
		return nil, fmt.Errorf("read the %s in %s: %w", DockerignoreName, root, err)
	}
	defer func() { _ = file.Close() }()

	ignored, err := ignorefile.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read the %s in %s: %w", DockerignoreName, root, err)
	}
	return append(excludes, ignored...), nil
}

func outsideTheContext(path string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if slices.Contains(neverInTheContext, segment) {
			return true
		}
	}
	return false
}
