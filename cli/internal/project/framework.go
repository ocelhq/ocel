package project

import (
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

func detectFramework(dir, entrypoint string) (language.App, error) {
	detected, found, err := language.DetectFramework(dir, entrypoint)
	if err != nil || found {
		return detected, err
	}
	return language.App{}, newInvalidConfigError(fmt.Errorf(
		"nothing in %s says what this app is built with: it contains no %s, so set \"framework\" to one of %s",
		dir, english.Or(language.ManifestNames()), english.Or(english.Quoted(buildoutput.Frameworks())),
	), "")
}

func frameworkOf(app, dir, named, entrypoint string) (language.App, error) {
	if named != "" {
		if !buildoutput.IsKnownFramework(named) {
			return language.App{}, newInvalidConfigError(fmt.Errorf("app %q declares framework %q, which nothing builds: the frameworks are %s", app, named, english.And(english.Quoted(buildoutput.Frameworks()))), "")
		}
		return language.App{Framework: named}, nil
	}
	if !isDir(dir) {
		return language.App{}, nil
	}
	detected, err := detectFramework(dir, entrypoint)
	if err != nil {
		return language.App{}, fmt.Errorf("app %q: %w", app, err)
	}
	return detected, nil
}

func architectureOf(app string, declared string) (string, error) {
	if declared == "" || declared == arch.X8664 || declared == arch.ARM64 {
		return declared, nil
	}
	return "", fmt.Errorf("app %q declares arch %q, which names no architecture: the architectures are %q and %q", app, declared, arch.X8664, arch.ARM64)
}
