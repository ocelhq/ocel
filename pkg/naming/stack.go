package naming

import (
	"fmt"
	"strings"
)

const (
	InfraApp              = "infra"
	containerWorkersField = "containers"
	pulumiProjectNS       = "ocel"
)

type StackName struct {
	Env     string
	App     string
	Release ReleaseToken

	containerWorkers bool
}

func InfraStack(env string) StackName {
	return StackName{Env: env, App: InfraApp}
}

func AppStack(env, app string, release ReleaseToken) StackName {
	return StackName{Env: env, App: app, Release: release}
}

func ContainerWorkersStack(env, app string) StackName {
	return StackName{Env: env, App: app, containerWorkers: true}
}

func appField(app string) string {
	if app == InfraApp {
		return ""
	}
	return app
}

func (s StackName) IsInfra() bool { return s.App == InfraApp }

func (s StackName) HoldsContainerWorkers() bool { return s.containerWorkers }

func (s StackName) IsZero() bool { return s.Env == "" && s.App == "" }

func (s StackName) String() string {
	if s.IsInfra() {
		return Join(FieldSeparator, s.Env, InfraApp)
	}
	if s.containerWorkers {
		return Join(FieldSeparator, s.Env, s.App, containerWorkersField)
	}
	return Join(FieldSeparator, s.Env, s.App, s.Release.String())
}

func ParseStackName(value string) (StackName, error) {
	fields := strings.Split(value, FieldSeparator)
	if len(fields) != 2 && len(fields) != 3 {
		return StackName{}, fmt.Errorf("stack name %q is none of %q, %q, %q and %q", value,
			"<env>--infra", "<env>--<app>", "<env>--<app>--<release>", "<env>--<app>--"+containerWorkersField)
	}
	if err := Validate("env", fields[0]); err != nil {
		return StackName{}, fmt.Errorf("stack name %q: %w", value, err)
	}
	if err := Validate("app", fields[1]); err != nil {
		return StackName{}, fmt.Errorf("stack name %q: %w", value, err)
	}
	env, app := fields[0], fields[1]
	switch {
	case len(fields) == 2:
		return StackName{Env: env, App: app}, nil
	case app == InfraApp:
		return StackName{}, fmt.Errorf("stack name %q uses the reserved app name %q", value, InfraApp)
	case fields[2] == containerWorkersField:
		return ContainerWorkersStack(env, app), nil
	}
	release, err := ParseReleaseToken(fields[2])
	if err != nil {
		return StackName{}, fmt.Errorf("stack name %q: %w", value, err)
	}
	return AppStack(env, app, release), nil
}

func PulumiProject(project string) string {
	return Join(WordSeparator, pulumiProjectNS, project)
}

func StateBackendURL(scheme, bucket, project string) string {
	return scheme + "://" + bucket + PathSeparator + SanitizeAlpha(project)
}
