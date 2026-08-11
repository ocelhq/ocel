package naming

import (
	"fmt"
	"strings"
)

const (
	InfraApp        = "infra"
	pulumiProjectNS = "ocel"
)

type StackName struct {
	Env     string
	App     string
	Release Release
}

func InfraStack(env string) StackName {
	return StackName{Env: env, App: InfraApp}
}

func AppStack(env, app string, release Release) StackName {
	return StackName{Env: env, App: app, Release: release}
}

func (s StackName) IsInfra() bool { return s.App == InfraApp }

func (s StackName) IsZero() bool { return s.Env == "" && s.App == "" }

func (s StackName) String() string {
	if s.IsInfra() {
		return Join(FieldSeparator, s.Env, InfraApp)
	}
	return Join(FieldSeparator, s.Env, s.App, s.Release.String())
}

func ParseStackName(value string) (StackName, error) {
	fields := strings.Split(value, FieldSeparator)
	switch {
	case len(fields) == 2 && fields[1] == InfraApp:
		if err := Validate("env", fields[0]); err != nil {
			return StackName{}, fmt.Errorf("stack name %q: %w", value, err)
		}
		return InfraStack(fields[0]), nil
	case len(fields) == 3:
		if err := Validate("env", fields[0]); err != nil {
			return StackName{}, fmt.Errorf("stack name %q: %w", value, err)
		}
		if err := Validate("app", fields[1]); err != nil {
			return StackName{}, fmt.Errorf("stack name %q: %w", value, err)
		}
		if fields[1] == InfraApp {
			return StackName{}, fmt.Errorf("stack name %q uses the reserved app name %q", value, InfraApp)
		}
		release, err := ParseRelease(fields[2])
		if err != nil {
			return StackName{}, fmt.Errorf("stack name %q: %w", value, err)
		}
		return AppStack(fields[0], fields[1], release), nil
	}
	return StackName{}, fmt.Errorf("stack name %q is neither %q nor %q", value, "<env>--infra", "<env>--<app>--<release>")
}

func PulumiProject(project string) string {
	return Join(WordSeparator, pulumiProjectNS, project)
}

func StateBackendURL(bucket, project string) string {
	return "s3://" + bucket + PathSeparator + SanitizeAlpha(project)
}
