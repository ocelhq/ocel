package build

import (
	"fmt"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type Host struct {
	NextRuntimeDir         string
	MaxFunctionBytes       int64
	NextRefreshesByRequest bool
	ShipsNextServerRuntime bool
}

func ReadHost(facts *contractv1.ProviderFacts) Host {
	return Host{
		NextRuntimeDir:         facts.GetNextRuntimeDir(),
		MaxFunctionBytes:       facts.GetMaxFunctionBytes(),
		NextRefreshesByRequest: facts.GetNextRefreshesByRequest(),
		ShipsNextServerRuntime: facts.GetShipsNextServerRuntime(),
	}
}

func FindNextFunctionApps(apps []project.App) []string {
	var names []string
	for _, a := range FunctionApps(apps) {
		if a.Framework() == buildoutput.FrameworkNext {
			names = append(names, a.Name)
		}
	}
	return names
}

func RefuseNextFunctionsWithoutRuntimeDir(cfg *project.Project, host Host) error {
	if host.NextRuntimeDir != "" {
		return nil
	}
	next := FindNextFunctionApps(cfg.Apps)
	if len(next) == 0 {
		return nil
	}
	const needs = "a Next app builds as functions only for a provider that names the directory its functions load Next's runtime files from"
	apps := english.And(english.Quoted(next))
	if cfg.Provider == nil {
		return fmt.Errorf("%s, and %s names no provider: name one there that does to build %s", needs, filepath.Base(cfg.Path), apps)
	}
	return fmt.Errorf("%s, and provider %q names none: deploy %s through one that does", needs, cfg.Provider.ID, apps)
}
