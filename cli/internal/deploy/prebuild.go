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
	return fmt.Sprintf("Would run %s before building: %s", preBuildName, command.Command)
}

func sayPrebuiltSkipsPreBuild(span *run.Span, command project.LifecycleCommand, tier environmentv1.Tier) {
	span.Say(fmt.Sprintf("--prebuilt skips %s; to run it, use `ocel run --env %s -- %s`", preBuildName, readiness.TierName(tier), command.Command))
}

func (d *deployRun) runPreBuild(ctx context.Context, command project.LifecycleCommand, forwards *portforward.Forwards, values map[string]build.AppVariables, resourceCount int) error {
	cfg := d.cfg
	env := map[string]string{}
	live := map[string]string{}
	maps.Copy(live, forwards.Bindings(portforward.WholeProject))
	maps.Copy(env, forwards.BindingProxyEnv(portforward.WholeProject))
	if command.App != "" {
		if err := build.RefuseBindingProxyNames(command.App, values[command.App]); err != nil {
			return fmt.Errorf("%s: %w", preBuildName, err)
		}
		maps.Copy(env, values[command.App].Env)
		maps.Copy(live, values[command.App].Live)
	}

	span := d.phase.Child(cfg.Slug, progress.Running.Title(preBuildName))
	if forwards == nil && resourceCount > 0 && !d.forwardsPorts {
		span.Say(fmt.Sprintf("%s runs without bindings: the provider forwards no port", preBuildName))
	}
	hidden := redaction.NewValues(build.AppVariables{Live: live, BindingProxyEnv: forwards.BindingProxyEnv(portforward.WholeProject)}.SecretValues())
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
	span.Warn(fmt.Sprintf("The package.json prebuild script in %s runs too, before each app's build; rename it if only %s should run",
		english.And(english.Quoted(apps)), preBuildName))
}
