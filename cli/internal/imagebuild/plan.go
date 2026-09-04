package imagebuild

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/railwayapp/railpack/core"
	"github.com/railwayapp/railpack/core/app"
	railpackplan "github.com/railwayapp/railpack/core/plan"

	"github.com/ocelhq/ocel/cli/internal/workspace"
)

const (
	PlanFileName = "railpack-plan.json"

	installStep = "install"
	buildStep   = "build"
)

func Plan(loc workspace.Location) ([]byte, error) {
	source, err := app.NewApp(loc.Root)
	if err != nil {
		return nil, fmt.Errorf("read %s as an app railpack can build: %w", loc.Root, err)
	}
	bare, err := app.FromEnvs(nil)
	if err != nil {
		return nil, err
	}
	commands := loc.Commands()
	result, err := core.GenerateBuildPlan(source, bare, &core.GenerateBuildPlanOptions{
		BuildCommand: commands.Build,
		StartCommand: commands.Start,
	})
	if err != nil {
		return nil, fmt.Errorf("railpack could not plan %s: %w", loc.Root, err)
	}
	if !result.Success {
		return nil, fmt.Errorf("railpack could not plan %s:\n%s", loc.Root, refusal(result))
	}
	scope(result.Plan, loc, commands)
	plan, err := json.Marshal(result.Plan)
	if err != nil {
		return nil, fmt.Errorf("serialize the railpack plan for %s: %w", loc.Root, err)
	}
	return plan, nil
}

func scope(built *railpackplan.BuildPlan, loc workspace.Location, commands workspace.Commands) {
	for i := range built.Steps {
		step := &built.Steps[i]
		step.Commands = slices.DeleteFunc(step.Commands, func(command railpackplan.Command) bool {
			copied, ok := command.(railpackplan.CopyCommand)
			return ok && copied.Image == "" && outsideTheContext(copied.Src)
		})
		switch step.Name {
		case installStep:
			replaceInstall(step, loc.Manager, commands.Install)
		case buildStep:
			if loc.InWorkspace() && commands.Build == "" {
				step.Commands = slices.DeleteFunc(step.Commands, func(command railpackplan.Command) bool {
					_, ok := command.(railpackplan.ExecCommand)
					return ok
				})
			}
		}
	}
}

func replaceInstall(step *railpackplan.Step, manager workspace.Manager, scoped string) {
	if scoped == "" {
		return
	}
	replaceable := workspace.ReplaceableInstalls(manager)
	for i, command := range step.Commands {
		exec, ok := command.(railpackplan.ExecCommand)
		if !ok || !slices.Contains(replaceable, exec.Cmd) {
			continue
		}
		exec.Cmd = scoped
		if exec.CustomName != "" {
			exec.CustomName = scoped
		}
		step.Commands[i] = exec
		return
	}
}

func refusal(result *core.BuildResult) string {
	var said []string
	for _, msg := range result.Logs {
		if line := strings.TrimSpace(msg.Msg); line != "" {
			said = append(said, "  "+line)
		}
	}
	if len(said) == 0 {
		return "  railpack recognised nothing to build in it"
	}
	return strings.Join(said, "\n")
}
