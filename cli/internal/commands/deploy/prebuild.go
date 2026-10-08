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
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/redaction"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const preBuildName = "lifecycle.preBuild"

func findPreBuild(cfg *project.Project, env *environmentv1.Environment) *project.LifecycleCommand {
	command := cfg.Lifecycle.PreBuild
	if command == nil || !command.RunsIn(env) {
		return nil
	}
	return command
}

func describePlannedPreBuild(command *project.LifecycleCommand, prebuilt bool) string {
	if command == nil || prebuilt {
		return ""
	}
	return fmt.Sprintf("Before it builds, this deploy would run %s: %s", preBuildName, command.Command)
}

func sayPrebuiltSkipsPreBuild(span *run.Span, command project.LifecycleCommand, tier environmentv1.Tier) {
	span.Say(fmt.Sprintf("--prebuilt skips %s, which runs before a build and this deploy builds nothing; if its command %q must run first, run it yourself, for example with `ocel run --env %s -- <command>`", preBuildName, command.Command, readiness.TierName(tier)))
}

func runPreBuild(ctx context.Context, a assembly, command project.LifecycleCommand, forwards *portforward.Forwards, values map[string]build.AppVariables, resourceCount int) error {
	cfg := a.cfg
	env := map[string]string{}
	live := map[string]string{}
	maps.Copy(live, forwards.Bindings(portforward.WholeProject))
	maps.Copy(env, forwards.RuntimeEnv())
	if command.App != "" {
		maps.Copy(env, values[command.App].Env)
		maps.Copy(live, values[command.App].Live)
	}

	span := a.phase.Child(cfg.Slug, progress.Running.Title(preBuildName))
	if forwards == nil && resourceCount > 0 && !a.infra.providerProcess.Facts().GetForwardsPorts() {
		span.Say(fmt.Sprintf("The provider forwards no port, so %s goes without the bindings of the resources %s declares", preBuildName, cfg.Slug))
	}
	hidden := redaction.NewValues(append(build.SecretValues(live), forwards.RuntimeEnv()[localrpc.SessionTokenEnvVar]))
	out := hidden.Writer(span.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED))
	err := lifecycle.Run(ctx, lifecycle.Command{
		Line:    command.Command,
		Dir:     filepath.Join(cfg.Dir, command.Path),
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
