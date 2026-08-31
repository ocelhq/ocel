package host

import (
	"strings"
	"testing"
)

func inspecting() map[string]string {
	return map[string]string{
		"what a release reads to see if the app is already serving": servingCommand(physical),
		"what a release that fell over captures as evidence":        stateCommand(physical),
		"what a release that fell over captures as logs":            logCommand(physical),
		"what a proxy that did not come up reports":                 containerRising(3),
		"what a bootstrap probes the proxy with":                    containerProbe(),
	}
}

func TestEveryInspectOnTheEvidencePathNamesTheFieldsItReads(t *testing.T) {
	t.Parallel()

	for what, command := range inspecting() {
		for at := 0; ; {
			found := strings.Index(command[at:], "docker inspect")
			if found < 0 {
				break
			}
			at += found + len("docker inspect")
			line, _, _ := strings.Cut(command[at:], "\n")
			if !strings.Contains(line, "--format") {
				t.Errorf("%s runs `docker inspect%s`, which prints the container's whole configuration — its environment among it — into a deploy's output",
					what, line)
			}
		}
	}
}

func TestNoInspectOnTheEvidencePathCanReachTheEnvironmentItWasHanded(t *testing.T) {
	t.Parallel()

	held := inspecting()
	held["the fact template a bootstrap compares the proxy against"] = ProxyFactTemplate
	for what, command := range held {
		for _, leak := range []string{".Config.Env", ".Env}}", "{{json .}}", "--format '{{.}}'"} {
			if strings.Contains(command, leak) {
				t.Errorf("%s reads %q, and %s prints every value the container was handed", what, command, leak)
			}
		}
		for at := 0; ; {
			found := strings.Index(command[at:], ".Config")
			if found < 0 {
				break
			}
			at += found + len(".Config")
			if !strings.HasPrefix(command[at:], ".Labels") && !strings.HasPrefix(command[at:], ".Image") {
				t.Errorf("%s reads %q and reaches into the container's configuration past the labels and the image it names", what, command)
			}
		}
	}
}

func TestTheLogsARefusalQuotesAreBounded(t *testing.T) {
	t.Parallel()

	command := logCommand(physical)
	if !strings.Contains(command, "--tail "+appLogTail) {
		t.Errorf("a refusal quotes %q, and an unbounded log is a deploy's whole output", command)
	}
}
