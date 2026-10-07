package project

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/configdoc"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

const DefaultLifecycleTimeout = 30 * time.Minute

type Previews string

const (
	PreviewsPersistent Previews = "persistent"
	PreviewsAll        Previews = "all"
	PreviewsNone       Previews = "none"
)

type Lifecycle struct {
	PreBuild *LifecycleCommand
}

type LifecycleCommand struct {
	Command  string
	App      string
	Previews Previews
	Timeout  time.Duration
}

func (c LifecycleCommand) RunsIn(env *environmentv1.Environment) bool {
	if env.GetTier() != environmentv1.Tier_TIER_PREVIEW {
		return true
	}
	switch c.Previews {
	case PreviewsAll:
		return true
	case PreviewsPersistent:
		return env.GetLifecycle() == environmentv1.Lifecycle_LIFECYCLE_PERSISTENT
	default:
		return false
	}
}

func normalizeLifecycle(raw *configdoc.LifecycleConfig, apps []App) (Lifecycle, error) {
	if raw == nil || raw.PreBuild == nil {
		return Lifecycle{}, nil
	}
	preBuild, err := normalizeLifecycleCommand(*raw.PreBuild, apps)
	if err != nil {
		return Lifecycle{}, fmt.Errorf("lifecycle.preBuild: %w", err)
	}
	return Lifecycle{PreBuild: &preBuild}, nil
}

func normalizeLifecycleCommand(raw configdoc.LifecycleCommand, apps []App) (LifecycleCommand, error) {
	command := strings.TrimSpace(raw.Command)
	if command == "" {
		return LifecycleCommand{}, fmt.Errorf("the command is empty: write the shell command to run, as in \"lifecycle\": { \"preBuild\": \"pnpm migrate\" }, or give the object a \"command\"")
	}

	previews := Previews(raw.Previews)
	switch previews {
	case "":
		previews = PreviewsPersistent
	case PreviewsPersistent, PreviewsAll, PreviewsNone:
	default:
		return LifecycleCommand{}, fmt.Errorf("previews is %q, want persistent, all or none", raw.Previews)
	}

	timeout := DefaultLifecycleTimeout
	if raw.Timeout != "" {
		parsed, err := time.ParseDuration(raw.Timeout)
		if err != nil || parsed <= 0 {
			return LifecycleCommand{}, fmt.Errorf("timeout is %q, want a positive duration such as 90s or 15m", raw.Timeout)
		}
		timeout = parsed
	}

	if raw.App != "" && !slices.ContainsFunc(apps, func(a App) bool { return a.Name == raw.App }) {
		names := make([]string, 0, len(apps))
		for _, a := range apps {
			names = append(names, a.Name)
		}
		return LifecycleCommand{}, fmt.Errorf("app is %q, which is no app of this project; the apps are %s", raw.App, strings.Join(names, ", "))
	}

	return LifecycleCommand{Command: command, App: raw.App, Previews: previews, Timeout: timeout}, nil
}
