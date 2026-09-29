package project

import (
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

func detectFramework(dir string) (string, error) {
	framework, found, err := language.DetectFramework(dir)
	if err != nil || found {
		return framework, err
	}
	return "", fmt.Errorf(
		"nothing in %s says what this app is built with: it contains no %s, so set \"framework\" to one of %s",
		dir, english.Or(language.ManifestNames()), english.Or(english.Quoted(buildoutput.Frameworks())),
	)
}

func frameworkOf(app string, dir string, named string) (string, error) {
	if named != "" {
		if !buildoutput.IsKnownFramework(named) {
			return "", fmt.Errorf("app %q declares framework %q, which nothing builds: the frameworks are %s", app, named, english.And(english.Quoted(buildoutput.Frameworks())))
		}
		return named, nil
	}
	if !isDir(dir) {
		return "", nil
	}
	framework, err := detectFramework(dir)
	if err != nil {
		return "", fmt.Errorf("app %q: %w", app, err)
	}
	return framework, nil
}

func architectureOf(app string, declared string) (string, error) {
	if declared == "" || declared == arch.X8664 || declared == arch.ARM64 {
		return declared, nil
	}
	return "", fmt.Errorf("app %q declares arch %q, which names no architecture: the architectures are %q and %q", app, declared, arch.X8664, arch.ARM64)
}
