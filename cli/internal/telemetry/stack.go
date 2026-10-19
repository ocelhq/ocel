package telemetry

import (
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
)

type Stack struct {
	Project           string
	Provider          string
	Frameworks        []string
	Languages         []string
	FrameworkVersions []string
	RuntimeVersions   []string
}

func NewStack(cfg *project.Project) Stack {
	if cfg == nil {
		return Stack{}
	}
	stack := Stack{Project: cfg.Slug}
	if cfg.Provider != nil {
		stack.Provider = cfg.Provider.ID
	}
	for _, app := range cfg.Apps {
		dir := filepath.Join(cfg.Dir, app.Path)
		framework, lang := app.Framework, app.Language
		if framework != "" {
			stack.Frameworks = append(stack.Frameworks, framework)
		}
		stack.Languages = append(stack.Languages, string(lang))
		if version := language.DeclaredFrameworkVersion(framework, dir); version != "" {
			stack.FrameworkVersions = append(stack.FrameworkVersions, framework+"@"+version)
		}
		if runtime, version := language.DeclaredRuntime(lang, dir, cfg.Dir); version != "" {
			stack.RuntimeVersions = append(stack.RuntimeVersions, runtime+"@"+version)
		}
	}
	return stack
}

func (s Stack) projectSlug() string { return s.Project }

func (s Stack) stackProperties() map[string]any {
	return map[string]any{
		"provider":           s.Provider,
		"frameworks":         sortedUnique(s.Frameworks),
		"languages":          sortedUnique(s.Languages),
		"framework_versions": sortedUnique(s.FrameworkVersions),
		"runtime_versions":   sortedUnique(s.RuntimeVersions),
	}
}
