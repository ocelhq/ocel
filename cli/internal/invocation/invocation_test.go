package invocation

import (
	"strings"
	"testing"
)

func lookupFrom(pairs map[string]string) func(string) string {
	return func(name string) string { return pairs[name] }
}

func TestDetectReportsTheAgentAndCIAnEnvironmentNames(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantAgent string
		wantCI    string
	}{
		{"nothing set", nil, "", ""},
		{"AI_AGENT names the agent", map[string]string{"AI_AGENT": "devin"}, "devin", ""},
		{"AI_AGENT as Claude Code sends it", map[string]string{"AI_AGENT": "claude-code_2-1-289_agent"}, "claude-code", ""},
		{"AI_AGENT with a dot falls through", map[string]string{"AI_AGENT": "some.agent", "CLAUDECODE": "1"}, "claude-code", ""},
		{"AI_AGENT with a dot and no marker is not an agent", map[string]string{"AI_AGENT": "some.agent"}, "", ""},
		{"AI_AGENT with underscores", map[string]string{"AI_AGENT": "claude-code_2_agent"}, "claude-code", ""},
		{"AI_AGENT with one underscore", map[string]string{"AI_AGENT": "goose_1"}, "goose", ""},
		{"AI_AGENT at 64 characters", map[string]string{"AI_AGENT": strings.Repeat("a", 64)}, strings.Repeat("a", 64), ""},
		{"AI_AGENT over 64 characters falls through", map[string]string{"AI_AGENT": strings.Repeat("a", 65), "CLAUDECODE": "1"}, "claude-code", ""},
		{"AI_AGENT with spaces falls through", map[string]string{"AI_AGENT": "my agent", "CODEX_CI": "1"}, "codex", ""},
		{"AI_AGENT with other punctuation falls through", map[string]string{"AI_AGENT": "a/b", "GEMINI_CLI": "1"}, "gemini-cli", ""},
		{"AI_AGENT leading underscore falls through", map[string]string{"AI_AGENT": "_x", "OPENCODE": "1"}, "opencode", ""},
		{"AI_AGENT 0 falls through", map[string]string{"AI_AGENT": "0", "CURSOR_AGENT": "1"}, "cursor", ""},
		{"AI_AGENT FALSE falls through", map[string]string{"AI_AGENT": "FALSE", "CURSOR_AGENT": "1"}, "cursor", ""},
		{"AI_AGENT No falls through", map[string]string{"AI_AGENT": "No"}, "", ""},
		{"AI_AGENT off falls through", map[string]string{"AI_AGENT": "off"}, "", ""},
		{"CLAUDECODE", map[string]string{"CLAUDECODE": "1"}, "claude-code", ""},
		{"CODEX_CI", map[string]string{"CODEX_CI": "1"}, "codex", ""},
		{"CODEX_SANDBOX", map[string]string{"CODEX_SANDBOX": "seatbelt"}, "codex", ""},
		{"CODEX_THREAD_ID", map[string]string{"CODEX_THREAD_ID": "abc"}, "codex", ""},
		{"GEMINI_CLI", map[string]string{"GEMINI_CLI": "1"}, "gemini-cli", ""},
		{"CURSOR_AGENT", map[string]string{"CURSOR_AGENT": "1"}, "cursor", ""},
		{"COPILOT_CLI", map[string]string{"COPILOT_CLI": "1"}, "copilot", ""},
		{"COPILOT_AGENT", map[string]string{"COPILOT_AGENT": "1"}, "copilot", ""},
		{"OPENCODE", map[string]string{"OPENCODE": "1"}, "opencode", ""},
		{"CLINE_ACTIVE", map[string]string{"CLINE_ACTIVE": "true"}, "cline", ""},
		{"AGENT amp", map[string]string{"AGENT": "amp"}, "amp", ""},
		{"AGENT other", map[string]string{"AGENT": "whatever"}, "agent", ""},
		{"AGENT 0", map[string]string{"AGENT": "0"}, "", ""},
		{"AGENT false", map[string]string{"AGENT": "false"}, "", ""},
		{"a marker set to 0 is not set", map[string]string{"CLAUDECODE": "0"}, "", ""},
		{"a marker set to false is not set", map[string]string{"CODEX_CI": "False", "GEMINI_CLI": "1"}, "gemini-cli", ""},
		{"a marker set to empty is not set", map[string]string{"CLAUDECODE": ""}, "", ""},
		{"GITHUB_ACTIONS", map[string]string{"GITHUB_ACTIONS": "true"}, "", "github-actions"},
		{"GITLAB_CI", map[string]string{"GITLAB_CI": "true"}, "", "gitlab"},
		{"CIRCLECI", map[string]string{"CIRCLECI": "true"}, "", "circleci"},
		{"BUILDKITE", map[string]string{"BUILDKITE": "true"}, "", "buildkite"},
		{"JENKINS_URL", map[string]string{"JENKINS_URL": "http://jenkins.local"}, "", "jenkins"},
		{"CI true", map[string]string{"CI": "true"}, "", "ci"},
		{"CI 1", map[string]string{"CI": "1"}, "", "ci"},
		{"CI false", map[string]string{"CI": "false"}, "", ""},
		{"CI 0", map[string]string{"CI": "0"}, "", ""},
		{"CI empty", map[string]string{"CI": ""}, "", ""},
		{"GITHUB_ACTIONS false is not set", map[string]string{"GITHUB_ACTIONS": "false", "CI": "true"}, "", "ci"},
		{"agent and ci are independent", map[string]string{"CLAUDECODE": "1", "GITHUB_ACTIONS": "true"}, "claude-code", "github-actions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, ci := Detect(lookupFrom(tt.env))
			if agent != tt.wantAgent || ci != tt.wantCI {
				t.Errorf("Detect = (%q, %q), want (%q, %q)", agent, ci, tt.wantAgent, tt.wantCI)
			}
		})
	}
}

func TestDetectPrefersTheFirstMatchingSource(t *testing.T) {
	order := []struct{ name, value, agent string }{
		{"AI_AGENT", "first_x", "first"},
		{"CLAUDECODE", "1", "claude-code"},
		{"CODEX_CI", "1", "codex"},
		{"GEMINI_CLI", "1", "gemini-cli"},
		{"CURSOR_AGENT", "1", "cursor"},
		{"COPILOT_CLI", "1", "copilot"},
		{"OPENCODE", "1", "opencode"},
		{"CLINE_ACTIVE", "1", "cline"},
		{"AGENT", "amp", "amp"},
	}
	for i := range order {
		env := map[string]string{}
		for _, later := range order[i:] {
			env[later.name] = later.value
		}
		if agent, _ := Detect(lookupFrom(env)); agent != order[i].agent {
			t.Errorf("with %s the first of %d set, agent = %q, want %q", order[i].name, len(order)-i, agent, order[i].agent)
		}
	}

	ciOrder := []struct{ name, ci string }{
		{"GITHUB_ACTIONS", "github-actions"},
		{"GITLAB_CI", "gitlab"},
		{"CIRCLECI", "circleci"},
		{"BUILDKITE", "buildkite"},
		{"JENKINS_URL", "jenkins"},
		{"CI", "ci"},
	}
	for i := range ciOrder {
		env := map[string]string{}
		for _, later := range ciOrder[i:] {
			env[later.name] = "true"
		}
		if _, ci := Detect(lookupFrom(env)); ci != ciOrder[i].ci {
			t.Errorf("with %s the first set, ci = %q, want %q", ciOrder[i].name, ci, ciOrder[i].ci)
		}
	}
}
