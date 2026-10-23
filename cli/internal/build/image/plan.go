package image

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	railpack "github.com/railwayapp/railpack/buildkit"
	"github.com/railwayapp/railpack/core"
	"github.com/railwayapp/railpack/core/app"
	railpackplan "github.com/railwayapp/railpack/core/plan"
	"github.com/railwayapp/railpack/core/providers/node"
	"github.com/tailscale/hujson"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/workspace"
)

const (
	PlanFileName = "railpack-plan.json"

	ConfigFileName = "railpack.json"

	installStep = "install"
	buildStep   = "build"

	railpackAllSecrets = "*"

	nodeProvider   = "node"
	goProvider     = "golang"
	pythonProvider = "python"
	rustProvider   = "rust"
	providerKey    = "provider"
	packagesKey    = "packages"
	goPackage      = "go"
	bunPackage     = "bun"
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
	commands, err := loc.Commands()
	if err != nil {
		return nil, err
	}
	options := &core.GenerateBuildPlanOptions{
		BuildCommand:             planBuildCommand(commands),
		StartCommand:             commands.Start,
		ErrorMissingStartCommand: true,
	}
	if provider := providerFor(loc); provider != "" {
		named, done, err := providerConfigFile(loc, commands, provider)
		if err != nil {
			return nil, err
		}
		defer done()
		options.ConfigFilePath = named
	}
	result, err := core.GenerateBuildPlan(source, bare, options)
	if err != nil {
		return nil, fmt.Errorf("railpack could not plan %s: %w", loc.Root, err)
	}
	if !result.Success {
		return nil, fmt.Errorf("railpack could not plan %s:\n%s", loc.Root, refusal(result))
	}
	outside, err := outsideTheContext(loc.Root)
	if err != nil {
		return nil, err
	}
	if err := scope(result.Plan, loc, commands, outside); err != nil {
		return nil, err
	}
	if commands.Asset.Name != "" {
		if err := addAsset(result.Plan, loc, commands.Asset); err != nil {
			return nil, err
		}
	}
	limitSecretsToBuildStep(result.Plan)
	plan, err := json.Marshal(result.Plan)
	if err != nil {
		return nil, fmt.Errorf("serialize the railpack plan for %s: %w", loc.Root, err)
	}
	return plan, nil
}

func planBuildCommand(commands workspace.Commands) string {
	if !commands.InstallsInBuild || commands.Install == "" {
		return commands.Build
	}
	if commands.Build == "" {
		return commands.Install
	}
	return commands.Install + " && " + commands.Build
}

func addAsset(built *railpackplan.BuildPlan, loc workspace.Location, asset workspace.Asset) error {
	for i := range built.Steps {
		step := &built.Steps[i]
		if step.Name != buildStep {
			continue
		}
		if step.Assets == nil {
			step.Assets = map[string]string{}
		}
		step.Assets[asset.Name] = asset.Content
		step.Commands = append(step.Commands, railpackplan.NewFileCommand(path.Join(railpack.WorkingDir, loc.Path, asset.Name), asset.Name))
		return nil
	}
	return fmt.Errorf("railpack planned %s with no %s step, and ocel writes %s, which the image starts with, in that step", loc.Root, buildStep, asset.Name)
}

func providerFor(loc workspace.Location) string {
	switch loc.Language {
	case language.Go:
		return goProvider
	case language.Python:
		return pythonProvider
	case language.JS:
		return nodeProvider
	case language.Rust:
		return rustProvider
	default:
		return ""
	}
}

func providerConfigFile(loc workspace.Location, commands workspace.Commands, provider string) (string, func(), error) {
	root := loc.Root
	config, err := configuredIn(root)
	if err != nil {
		return "", nil, err
	}
	config[providerKey] = json.RawMessage(`"` + provider + `"`)
	if loc.Language == language.Go && loc.ModuleDir != "." && loc.GoVersion != "" {
		if err := pinPackage(config, goPackage, loc.GoVersion, root); err != nil {
			return "", nil, err
		}
	}
	for _, tool := range commands.Tools {
		if err := pinPackage(config, tool.Name, tool.Version, root); err != nil {
			return "", nil, err
		}
	}
	if loc.Language == language.JS && loc.PackageManager != workspace.Bun {
		callsBun, err := loc.HasScriptCallingBun()
		if err != nil {
			return "", nil, err
		}
		if callsBun {
			if err := pinPackage(config, bunPackage, node.DEFAULT_BUN_VERSION, root); err != nil {
				return "", nil, err
			}
		}
	}
	written, err := json.Marshal(config)
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "ocel-railpack-config-")
	if err != nil {
		return "", nil, err
	}
	discard := func() { _ = os.RemoveAll(dir) }
	file := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(file, written, 0o600); err != nil {
		discard()
		return "", nil, err
	}
	named, err := filepath.Rel(root, file)
	if err != nil {
		discard()
		return "", nil, err
	}
	return named, discard, nil
}

func pinPackage(config map[string]json.RawMessage, name, version, root string) error {
	packages := map[string]string{}
	if declared, ok := config[packagesKey]; ok {
		if err := json.Unmarshal(declared, &packages); err != nil {
			return fmt.Errorf("read the %s of the %s in %s: %w", packagesKey, ConfigFileName, root, err)
		}
	}
	if _, pinned := packages[name]; pinned {
		return nil
	}
	packages[name] = version
	pinned, err := json.Marshal(packages)
	if err != nil {
		return err
	}
	config[packagesKey] = pinned
	return nil
}

func configuredIn(root string) (map[string]json.RawMessage, error) {
	config := map[string]json.RawMessage{}
	named := filepath.Join(root, ConfigFileName)
	read, err := os.ReadFile(named)
	if os.IsNotExist(err) {
		return config, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the %s in %s: %w", ConfigFileName, root, err)
	}
	standard, err := hujson.Standardize(read)
	if err != nil {
		return nil, fmt.Errorf("read %s as JSON: %w", named, err)
	}
	if err := json.Unmarshal(standard, &config); err != nil {
		return nil, fmt.Errorf("read %s as JSON: %w", named, err)
	}
	return config, nil
}

func scope(built *railpackplan.BuildPlan, loc workspace.Location, commands workspace.Commands, outside func(string) bool) error {
	for i := range built.Steps {
		step := &built.Steps[i]
		step.Commands = slices.DeleteFunc(step.Commands, func(command railpackplan.Command) bool {
			copied, ok := command.(railpackplan.CopyCommand)
			return ok && copied.Image == "" && outside(copied.Src)
		})
		switch step.Name {
		case installStep:
			if commands.InstallsInBuild {
				step.Commands = slices.DeleteFunc(step.Commands, isExec)
			} else if err := replaceInstall(step, loc, commands.Install); err != nil {
				return err
			}
		case buildStep:
			if loc.Member && commands.Build == "" {
				step.Commands = slices.DeleteFunc(step.Commands, isExec)
			}
		}
	}
	return nil
}

func isExec(command railpackplan.Command) bool {
	_, ok := command.(railpackplan.ExecCommand)
	return ok
}

func replaceInstall(step *railpackplan.Step, loc workspace.Location, scoped string) error {
	if scoped == "" {
		return nil
	}
	replaceable := loc.ReplaceableInstalls()
	var ran []string
	for i, command := range step.Commands {
		exec, ok := command.(railpackplan.ExecCommand)
		if !ok {
			continue
		}
		if !slices.Contains(replaceable, exec.Cmd) {
			ran = append(ran, exec.Cmd)
			continue
		}
		exec.Cmd = scoped
		if exec.CustomName != "" {
			exec.CustomName = scoped
		}
		step.Commands[i] = exec
		return nil
	}
	return fmt.Errorf(
		"railpack installs %s with %s, and ocel replaces that with %q to install only what app %q reaches: nothing in the install step is a command ocel knows how to scope, so the whole workspace at %s would be installed to serve one app",
		loc.Root, quoted(ran), scoped, loc.Path, loc.Root,
	)
}

func quoted(commands []string) string {
	if len(commands) == 0 {
		return "nothing ocel can read as a command"
	}
	said := make([]string, 0, len(commands))
	for _, command := range commands {
		said = append(said, fmt.Sprintf("%q", command))
	}
	return strings.Join(said, ", ")
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

func limitSecretsToBuildStep(built *railpackplan.BuildPlan) {
	for i := range built.Steps {
		built.Steps[i].Secrets = nil
		if built.Steps[i].Name == buildStep {
			built.Steps[i].Secrets = []string{railpackAllSecrets}
		}
	}
}
