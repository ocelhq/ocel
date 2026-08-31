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
		"what a preflight reads the proxy's state with":             stateCommand(ProxyContainer),
		"what a preflight reads the disk headroom with":             headroomCommand([]string{"ocel-shop-web"}),
	}
}

func TestEveryInspectOnTheEvidencePathNamesTheFieldsItReads(t *testing.T) {
	t.Parallel()

	read := 0
	for what, command := range inspecting() {
		for at := 0; ; {
			found := strings.Index(command[at:], "inspect")
			if found < 0 {
				break
			}
			at += found + len("inspect")
			read++
			line, _, _ := strings.Cut(command[at:], "\n")
			if !strings.Contains(line, "--format") {
				t.Errorf("%s runs `inspect%s`, which prints the whole of what it inspects — a container's environment among it — into a deploy's output",
					what, line)
			}
		}
	}
	if read < len(inspecting()) {
		t.Fatalf("this guard read %d inspects across %d commands, so it is passing over commands it never looked at", read, len(inspecting()))
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

func TestEveryInspectThisPackageRunsIsHeldToTheSelectorRules(t *testing.T) {
	t.Parallel()

	rendered(t, "docker inspect", []string{"containerProbe", "containerRising", "servingCommand", "stateCommand"},
		"an inspect rendered somewhere this bench does not read is held to none of the rules in this file, and a bare one prints every value a container was handed")
}

func TestTheLogsARefusalQuotesAreBounded(t *testing.T) {
	t.Parallel()

	command := logCommand(physical)
	if !strings.Contains(command, "--tail "+appLogTail) {
		t.Errorf("a refusal quotes %q, and an unbounded log is a deploy's whole output", command)
	}
}

func certifying() map[string]string {
	return map[string]string{
		"what doctor reads a served leaf with":   words(helperCommand("leaf", "shop.example.com")),
		"what a pinned pair is read off":         "cat " + quoted(PinCertificate(ProxyPins+"/wildcard")),
		"what a hostname's pair is forgotten by": words(helperCommand("forget", "shop.example.com")),
	}
}

func TestNothingOnTheCertificatePathReadsTheProxysDataDirectory(t *testing.T) {
	t.Parallel()

	named := 0
	for what, command := range certifying() {
		if !strings.Contains(command, "shop.example.com") && !strings.Contains(command, ProxyPins) {
			t.Fatalf("%s runs %q, which names neither the hostname nor %s, so this guard is reading a command that does nothing", what, command, ProxyPins)
		}
		named++
		if strings.Contains(command, ProxyData) {
			t.Errorf("%s runs %q and reaches into %s: caddy's storage layout is undocumented as an interface and is what a version bump rearranges, and expiry is read off the served leaf instead",
				what, command, ProxyData)
		}
	}
	if named != len(certifying()) {
		t.Fatalf("this guard read %d of %d commands", named, len(certifying()))
	}
}
