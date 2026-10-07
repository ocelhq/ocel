package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/lifecycle"
	"github.com/ocelhq/ocel/cli/internal/portforward"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/redaction"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const preBuildName = "lifecycle.preBuild"

func preBuildFor(cfg *project.Project, env *environmentv1.Environment) *project.LifecycleCommand {
	command := cfg.Lifecycle.PreBuild
	if command == nil || !command.RunsIn(env) {
		return nil
	}
	return command
}

func (a assembly) preBuild() *project.LifecycleCommand {
	if a.prebuilt {
		return nil
	}
	return preBuildFor(a.cfg, a.env)
}

func preBuildPlanNote(cfg *project.Project, env *environmentv1.Environment, prebuilt bool) string {
	command := preBuildFor(cfg, env)
	if command == nil || prebuilt {
		return ""
	}
	return fmt.Sprintf("Before it builds, this deploy would run %s: %s", preBuildName, command.Command)
}

func sayPrebuiltSkipsPreBuild(span *run.Span, cfg *project.Project, env *environmentv1.Environment) {
	command := preBuildFor(cfg, env)
	if command == nil {
		return
	}
	span.Say(fmt.Sprintf("--prebuilt skips %s, which runs before a build and this deploy builds nothing; if its command %q must run first, run it yourself, for example with `ocel run --env %s -- <command>`", preBuildName, command.Command, tierName(env)))
}

func tierName(env *environmentv1.Environment) string {
	if env.GetTier() == environmentv1.Tier_TIER_PREVIEW {
		return "preview"
	}
	return "production"
}

func runPreBuild(ctx context.Context, a assembly, command project.LifecycleCommand, forwards *portforward.Forwards, values map[string]build.AppVariables, resources int) error {
	cfg := a.cfg
	dir := cfg.Dir
	env := map[string]string{}
	live := map[string]string{}
	maps.Copy(live, forwards.Bindings(portforward.WholeProject))
	if command.App != "" {
		app, _ := findApp(cfg, command.App)
		dir = filepath.Join(cfg.Dir, app.Path)
		maps.Copy(env, values[command.App].Env)
		maps.Copy(live, values[command.App].Live)
	}

	span := a.phase.Child(cfg.Slug, progress.Running.Title(preBuildName))
	if forwards == nil && resources > 0 && !a.infra.providerProcess.Facts().GetForwardsPorts() {
		span.Say(fmt.Sprintf("The provider forwards no port, so %s goes without the bindings of the resources %s declares", preBuildName, cfg.Slug))
	}
	hidden := redaction.NewValues(build.SecretValues(live))
	out := hidden.Writer(span.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED))
	err := lifecycle.Run(ctx, lifecycle.Command{
		Shell:   command.Command,
		Dir:     dir,
		Env:     env,
		Live:    live,
		Timeout: command.Timeout,
		Stdout:  out,
		Stderr:  out,
	})
	if flushed := out.Flush(); err == nil {
		err = flushed
	}
	if err != nil {
		err = hidden.HideError(fmt.Errorf("%s: %w", preBuildName, err))
	}
	span.End(err)
	return err
}

func findApp(cfg *project.Project, name string) (project.App, bool) {
	for _, app := range cfg.Apps {
		if app.Name == name {
			return app, true
		}
	}
	return project.App{}, false
}

func warnNpmPrebuildScripts(span *run.Span, cfg *project.Project) {
	var apps []string
	for _, app := range cfg.Apps {
		raw, err := os.ReadFile(filepath.Join(cfg.Dir, app.Path, "package.json"))
		if err != nil {
			continue
		}
		var manifest struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(raw, &manifest) == nil && strings.TrimSpace(manifest.Scripts["prebuild"]) != "" {
			apps = append(apps, app.Name)
		}
	}
	if len(apps) == 0 {
		return
	}
	span.Warn(fmt.Sprintf("A package.json prebuild script is set in %s. npm and yarn 1 run it before each app's own build with the build's environment, and %s runs once per deploy against the deployed bindings, so both run. Rename the script if only %s should run it",
		english.And(english.Quoted(apps)), preBuildName, preBuildName))
}
