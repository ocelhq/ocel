package invocation

import (
	"regexp"
	"strings"
)

var agentIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type marker struct {
	names []string
	label string
}

var agentMarkers = []marker{
	{[]string{"CLAUDECODE"}, "claude-code"},
	{[]string{"CODEX_CI", "CODEX_SANDBOX", "CODEX_THREAD_ID"}, "codex"},
	{[]string{"GEMINI_CLI"}, "gemini-cli"},
	{[]string{"CURSOR_AGENT"}, "cursor"},
	{[]string{"COPILOT_CLI", "COPILOT_AGENT"}, "copilot"},
	{[]string{"OPENCODE"}, "opencode"},
	{[]string{"CLINE_ACTIVE"}, "cline"},
}

var ciMarkers = []marker{
	{[]string{"GITHUB_ACTIONS"}, "github-actions"},
	{[]string{"GITLAB_CI"}, "gitlab"},
	{[]string{"CIRCLECI"}, "circleci"},
	{[]string{"BUILDKITE"}, "buildkite"},
	{[]string{"JENKINS_URL"}, "jenkins"},
	{[]string{"CI"}, "ci"},
}

func Detect(getenv func(string) string) (agent, ci string) {
	return detectAgent(getenv), detectCI(getenv)
}

func detectAgent(getenv func(string) string) string {
	if value := getenv("AI_AGENT"); isSet(value) && agentIdentifier.MatchString(value) {
		name, _, _ := strings.Cut(value, "_")
		if name != "" {
			return name
		}
	}
	if label := findMarkedLabel(getenv, agentMarkers); label != "" {
		return label
	}
	switch value := getenv("AGENT"); {
	case !isSet(value):
		return ""
	case value == "amp":
		return "amp"
	default:
		return "agent"
	}
}

func detectCI(getenv func(string) string) string {
	return findMarkedLabel(getenv, ciMarkers)
}

func findMarkedLabel(getenv func(string) string, markers []marker) string {
	for _, m := range markers {
		for _, name := range m.names {
			if isSet(getenv(name)) {
				return m.label
			}
		}
	}
	return ""
}

func isSet(value string) bool {
	switch strings.ToLower(value) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}
